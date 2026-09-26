// Throwaway load test for the agents endpoints. Serves the production
// middleware chain + API-key auth + the real agents stack over a loopback TCP
// listener, against the dev database, with a temporary user that is deleted
// (cascading to every agent it made) on exit.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/apikeys"
	"whatsapp-ai-caller-server/internal/config"
	"whatsapp-ai-caller-server/internal/db"
	"whatsapp-ai-caller-server/internal/handlers"
	appmiddleware "whatsapp-ai-caller-server/internal/middleware"
	"whatsapp-ai-caller-server/internal/users"
)

const (
	seedAgents = 64
	stepDur    = 4 * time.Second
)

var levels = []int{1, 8, 32, 64}

var (
	pool    *pgxpool.Pool
	baseURL string
	client  = &http.Client{Transport: &http.Transport{
		MaxIdleConns: 512, MaxIdleConnsPerHost: 512, IdleConnTimeout: time.Minute,
	}}
)

type req struct {
	method, path, body, key string
	want                    int
	onBody                  func(string)
}

func main() {
	ctx := context.Background()
	cfg := config.Load()
	var err error
	pool, err = db.Connect(ctx, cfg.DatabaseURL)
	must(err)
	defer pool.Close()

	agentsRepo := agents.NewRepository(pool)
	usersRepo := users.NewRepository(pool, agentsRepo.CreateDefault)
	keysRepo := apikeys.NewRepository(pool)

	clerkID := fmt.Sprintf("loadtest_%d", time.Now().UnixNano())
	user, err := usersRepo.Upsert(ctx, users.UpsertParams{ClerkID: clerkID, Email: clerkID + "@example.invalid"})
	must(err)
	defer func() {
		must(usersRepo.DeleteByClerkID(context.Background(), clerkID))
		var left int
		must(pool.QueryRow(context.Background(), `SELECT count(*) FROM agents WHERE user_id = $1`, user.ID).Scan(&left))
		fmt.Printf("\ncleanup: temp user deleted, %d of its agents left behind\n", left)
	}()

	// One shared key, plus one key per worker, to compare the auth row-lock
	// contention (Authenticate UPDATEs api_keys.last_used_at on every request).
	shared, err := keysRepo.Create(ctx, user.ID, "load-shared")
	must(err)
	perWorker := make([]string, 64)
	for i := range perWorker {
		k, err := keysRepo.Create(ctx, user.ID, fmt.Sprintf("load-%d", i))
		must(err)
		perWorker[i] = k.Key
	}
	keyShared := func(int) string { return shared.Key }
	keyOwn := func(w int) string { return perWorker[w%len(perWorker)] }

	// Production middleware chain (request logging kept, its output discarded)
	// + the real agents controller/service/repository.
	chimiddleware.DefaultLogger = chimiddleware.RequestLogger(&chimiddleware.DefaultLogFormatter{Logger: discard{}, NoColor: true})
	c := agents.NewController(agents.NewService(agentsRepo, nil, nil))
	auth := appmiddleware.NewAuth(usersRepo, keysRepo)
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID, chimiddleware.RealIP, chimiddleware.Logger, chimiddleware.Recoverer,
		appmiddleware.CORS(cfg.CORSAllowedOrigins))
	r.Get("/health", handlers.HealthCheck)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAPIKeyUser)
		r.Get("/authonly", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		r.Post("/v1/agents", c.Create)
		r.Get("/v1/agents", c.List)
		r.Get("/v1/agents/{agent_id}", c.Get)
		r.Patch("/v1/agents/{agent_id}", c.Update)
		r.Delete("/v1/agents/{agent_id}", c.Delete)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	baseURL = srv.URL

	// Seed one agent per potential worker so PATCH never contends on a row.
	ids := make([]string, 0, seedAgents)
	for i := 0; i < seedAgents; i++ {
		code, body := do(req{method: "POST", path: "/v1/agents", key: shared.Key,
			body: fmt.Sprintf(`{"agent":{"name":"seed-%02d"},"prompt":{"system_prompt":"You are a helpful assistant used for load testing."}}`, i)})
		if code != http.StatusCreated {
			panic(fmt.Sprintf("seed failed: %d %s", code, body))
		}
		ids = append(ids, idOf(body))
	}
	for i := 0; i < 300; i++ { // warm-up: TCP conns, pgx conns, prepared statements
		do(req{method: "GET", path: "/v1/agents", key: shared.Key})
	}
	fmt.Printf("pool max conns %d | seeded %d agents (+1 starter) | %s per step\n",
		pool.Config().MaxConns, len(ids), stepDur)

	fmt.Printf("\n%-36s %4s %8s %9s %9s %9s %9s %7s %5s %9s\n",
		"scenario", "conc", "req/s", "p50", "p90", "p99", "max", "reqs", "errs", "poolwait")

	scenario("GET /health (no DB)", func(w int, _ int64) req {
		return req{method: "GET", path: "/health", want: 200}
	})
	scenario("auth only, shared key", func(w int, _ int64) req {
		return req{method: "GET", path: "/authonly", key: keyShared(w), want: 204}
	})
	scenario("auth only, key per worker", func(w int, _ int64) req {
		return req{method: "GET", path: "/authonly", key: keyOwn(w), want: 204}
	})
	scenario("GET list (page of 20), shared key", func(w int, _ int64) req {
		return req{method: "GET", path: "/v1/agents", key: keyShared(w), want: 200}
	})
	scenario("GET list (page of 20), key/worker", func(w int, _ int64) req {
		return req{method: "GET", path: "/v1/agents", key: keyOwn(w), want: 200}
	})
	scenario("GET one, shared key", func(w int, _ int64) req {
		return req{method: "GET", path: "/v1/agents/" + ids[w%len(ids)], key: keyShared(w), want: 200}
	})
	scenario("GET one, key/worker", func(w int, _ int64) req {
		return req{method: "GET", path: "/v1/agents/" + ids[w%len(ids)], key: keyOwn(w), want: 200}
	})
	scenario("PATCH (own agent), key/worker", func(w int, i int64) req {
		return req{method: "PATCH", path: "/v1/agents/" + ids[w%len(ids)], key: keyOwn(w), want: 200,
			body: fmt.Sprintf(`{"prompt":{"system_prompt":"load test revision %d"},"model":{"temperature":0.4}}`, i)}
	})

	var (
		mu      sync.Mutex
		created []string
		seq     atomic.Int64
	)
	scenario("POST create, key/worker", func(w int, _ int64) req {
		return req{method: "POST", path: "/v1/agents", key: keyOwn(w), want: 201,
			body: fmt.Sprintf(`{"agent":{"name":"load-%d"},"model":{"temperature":0.5}}`, seq.Add(1)),
			onBody: func(b string) {
				mu.Lock()
				created = append(created, idOf(b))
				mu.Unlock()
			}}
	})

	// DELETE consumes what POST created, split evenly across the levels. It can
	// run out before the step's time is up; the rest go with the temp user.
	share := len(created) / len(levels)
	fmt.Printf("  (POST created %d agents; each DELETE level may delete up to %d)\n", len(created), share)
	for li, conc := range levels {
		batch := make(chan string, share)
		for _, id := range created[li*share : (li+1)*share] {
			batch <- id
		}
		close(batch)
		runLevel("DELETE, key/worker", conc, func(w int, _ int64) (req, bool) {
			id, ok := <-batch
			return req{method: "DELETE", path: "/v1/agents/" + id, key: keyOwn(w), want: 200}, ok
		})
	}
}

func scenario(name string, gen func(w int, i int64) req) {
	for _, conc := range levels {
		runLevel(name, conc, func(w int, i int64) (req, bool) { return gen(w, i), true })
	}
}

// runLevel drives conc closed-loop workers for stepDur (or until gen runs dry)
// and prints one result row.
func runLevel(name string, conc int, gen func(w int, i int64) (req, bool)) {
	before := pool.Stat()
	deadline := time.Now().Add(stepDur)
	var counter atomic.Int64
	lats := make([][]time.Duration, conc)
	errs := make([]int, conc)
	firstErr := make([]string, conc)

	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				q, ok := gen(w, counter.Add(1))
				if !ok {
					return
				}
				t0 := time.Now()
				code, body := do(q)
				lats[w] = append(lats[w], time.Since(t0))
				if code != q.want {
					errs[w]++
					if firstErr[w] == "" {
						firstErr[w] = fmt.Sprintf("%d %s", code, trunc(body))
					}
				} else if q.onBody != nil {
					q.onBody(body)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	after := pool.Stat()

	var all []time.Duration
	totalErrs, sampleErr := 0, ""
	for w := range lats {
		all = append(all, lats[w]...)
		totalErrs += errs[w]
		if sampleErr == "" {
			sampleErr = firstErr[w]
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	pct := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		return all[min(len(all)-1, int(p*float64(len(all))))]
	}
	wait := "-"
	if acq := after.AcquireCount() - before.AcquireCount(); acq > 0 {
		wait = ms((after.AcquireDuration() - before.AcquireDuration()) / time.Duration(acq))
	}
	fmt.Printf("%-36s %4d %8.0f %9s %9s %9s %9s %7d %5d %9s\n", name, conc,
		float64(len(all))/elapsed.Seconds(), ms(pct(.50)), ms(pct(.90)), ms(pct(.99)), ms(pct(1)),
		len(all), totalErrs, wait)
	if sampleErr != "" {
		fmt.Printf("    first error: %s\n", sampleErr)
	}
}

func do(q req) (int, string) {
	hr, _ := http.NewRequest(q.method, baseURL+q.path, bytes.NewBufferString(q.body))
	if q.key != "" {
		hr.Header.Set("Authorization", "Bearer "+q.key)
	}
	if q.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(hr)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func idOf(body string) string {
	var env struct{ Data struct{ ID string } }
	_ = json.Unmarshal([]byte(body), &env)
	return env.Data.ID
}

type discard struct{}

func (discard) Print(...any) {}

func ms(d time.Duration) string { return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000) }

func trunc(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		panic(err)
	}
}
