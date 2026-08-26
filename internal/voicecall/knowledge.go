package voicecall

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

const (
	// knowledgeToolName is the function the model calls to look something up. It
	// is the same name on every provider, so one prompt and one runner cover the
	// Realtime session and the request-based pipelines alike.
	knowledgeToolName = "search_knowledge_base"

	// knowledgeToolTopK is how many passages one lookup returns. A spoken answer
	// is one or two sentences, so a handful of passages is all the model can use;
	// more only adds tokens to generate through before it starts talking.
	knowledgeToolTopK = 4

	// knowledgeToolTimeout bounds one lookup. The caller is sitting in silence
	// while it runs, so a slow vector store has to become "I don't have that"
	// quickly rather than holding the turn open.
	knowledgeToolTimeout = 6 * time.Second

	// knowledgeToolMaxChars bounds the retrieved text handed back to the model,
	// for the same reason: everything here is read before the reply begins.
	knowledgeToolMaxChars = 3000
)

// KnowledgeRetriever reads an agent's knowledge bases back during a call.
// Implemented by internal/knowledgebases.Retriever; an interface so voicecall
// does not depend on how the vectors are stored.
type KnowledgeRetriever interface {
	// Search returns the passages across namespaces that best match query, most
	// relevant first, at most topK of them.
	Search(ctx context.Context, namespaces []string, query string, topK int) ([]models.KnowledgeSnippet, error)
	// Enabled reports whether retrieval is configured at all.
	Enabled() bool
}

// toolDefinition is one function the model may call, in provider-neutral form.
// Each provider renders it into its own schema shape.
type toolDefinition struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object describing the arguments.
	Parameters map[string]any
}

// toolRunner is the set of tools available on one call. Run never returns an
// error: a lookup that fails is reported to the model as text, so the agent can
// say it could not find something instead of the turn dying silently.
type toolRunner interface {
	Definitions() []toolDefinition
	Run(ctx context.Context, name, arguments string) string
}

// knowledgeToolbox exposes one agent's attached knowledge bases as a callable
// tool for the length of a call. The namespaces are resolved once, when the call
// is answered, so a lookup mid-call is a vector query and nothing else.
type knowledgeToolbox struct {
	retriever  KnowledgeRetriever
	namespaces []string
	names      []string
	callID     string
}

// newKnowledgeToolbox builds the toolbox for one call, or returns nil when
// there is nothing to look anything up in: no retriever configured, or an agent
// with no knowledge bases attached. A nil toolbox is the "no tools" case
// everywhere below, so no caller has to special-case an agent that answers from
// its prompt alone.
func newKnowledgeToolbox(retriever KnowledgeRetriever, bases []models.AgentKnowledgeBase, callID string) *knowledgeToolbox {
	if retriever == nil || !retriever.Enabled() || len(bases) == 0 {
		return nil
	}
	box := &knowledgeToolbox{retriever: retriever, callID: callID}
	for _, base := range bases {
		namespace := strings.TrimSpace(base.Namespace)
		if namespace == "" {
			continue
		}
		box.namespaces = append(box.namespaces, namespace)
		if name := strings.TrimSpace(base.Name); name != "" {
			box.names = append(box.names, name)
		}
	}
	if len(box.namespaces) == 0 {
		return nil
	}
	return box
}

// Definitions describes the lookup function to the model. The knowledge bases
// are named in the description so the model can tell whether a question is one
// this agent can look up at all.
func (t *knowledgeToolbox) Definitions() []toolDefinition {
	if t == nil {
		return nil
	}
	description := "Search the knowledge base for information needed to answer the caller. " +
		"Call this whenever the caller asks about anything specific that is not already in this conversation."
	if len(t.names) > 0 {
		description += " It covers: " + strings.Join(t.names, ", ") + "."
	}
	return []toolDefinition{{
		Name:        knowledgeToolName,
		Description: description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "What to look up, as a short natural-language question in the caller's own words.",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}}
}

// Instructions is appended to the agent's prompt when the toolbox is active, so
// the model both knows the lookup exists and knows not to narrate using it —
// on a phone call "let me search my knowledge base" is dead air with a sentence
// over it.
func (t *knowledgeToolbox) Instructions() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("You can look things up with the ")
	b.WriteString(knowledgeToolName)
	b.WriteString(" tool")
	if len(t.names) > 0 {
		b.WriteString(", which covers: ")
		b.WriteString(strings.Join(t.names, ", "))
	}
	b.WriteString(". Use it before answering any question about those topics, and answer from what it returns rather than from memory. ")
	b.WriteString("If it returns nothing relevant, say you do not have that information instead of guessing. ")
	b.WriteString("Never mention the tool, the search, or the knowledge base to the caller — just answer.")
	return b.String()
}

// Run executes one tool call and returns the text the model reads as the
// result.
func (t *knowledgeToolbox) Run(ctx context.Context, name, arguments string) string {
	if t == nil {
		return "No knowledge base is available on this call."
	}
	if name != knowledgeToolName {
		return fmt.Sprintf("Unknown tool %q.", name)
	}

	var args struct {
		Query string `json:"query"`
	}
	// Arguments arrive as a JSON string built token by token, so a truncated or
	// malformed one is a real possibility; asking the model to retry beats
	// answering from an empty query.
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &args); err != nil {
		log.Printf("voicecall: call %s knowledge lookup received malformed arguments %q: %v", t.callID, arguments, err)
		return "The search arguments could not be read. Retry with a plain question in the query field."
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "No search query was given."
	}

	ctx, cancel := context.WithTimeout(ctx, knowledgeToolTimeout)
	defer cancel()

	startedAt := time.Now()
	snippets, err := t.retriever.Search(ctx, t.namespaces, query, knowledgeToolTopK)
	if err != nil {
		log.Printf("voicecall: call %s knowledge lookup %q failed after %dms: %v", t.callID, query, time.Since(startedAt).Milliseconds(), err)
		return "The knowledge base could not be reached. Tell the caller you cannot look that up right now."
	}
	// The best score is logged because it is what separates "the knowledge base
	// answered" from "the knowledge base was searched and had nothing" — without
	// it, a lookup that returned only weak matches looks identical to one that
	// worked.
	var topScore float32
	if len(snippets) > 0 {
		topScore = snippets[0].Score
	}
	log.Printf("voicecall: call %s knowledge lookup %q returned %d passages (top score %.3f) from %d namespace(s) in %dms",
		t.callID, query, len(snippets), topScore, len(t.namespaces), time.Since(startedAt).Milliseconds())
	if len(snippets) == 0 {
		return "No relevant information was found in the knowledge base."
	}
	return formatKnowledgeSnippets(snippets)
}

// formatKnowledgeSnippets renders retrieved passages for the model: each one
// titled with the source it came from, so a spoken answer can attribute it, and
// the whole thing bounded so a long chunk cannot push the reply's latency up on
// its own.
func formatKnowledgeSnippets(snippets []models.KnowledgeSnippet) string {
	var b strings.Builder
	for i, snippet := range snippets {
		text := strings.TrimSpace(snippet.Text)
		if text == "" {
			continue
		}
		entry := fmt.Sprintf("[%d] %s\n%s\n\n", i+1, knowledgeSnippetTitle(snippet, i), text)
		if b.Len()+len(entry) > knowledgeToolMaxChars {
			if b.Len() == 0 {
				// The first passage alone is over the budget; a truncated answer is
				// still better than none.
				b.WriteString(entry[:knowledgeToolMaxChars])
			}
			break
		}
		b.WriteString(entry)
	}
	result := strings.TrimSpace(b.String())
	if result == "" {
		return "No relevant information was found in the knowledge base."
	}
	return result
}

func knowledgeSnippetTitle(snippet models.KnowledgeSnippet, index int) string {
	if title := strings.TrimSpace(snippet.Title); title != "" {
		return title
	}
	return fmt.Sprintf("Untitled source %d", index+1)
}
