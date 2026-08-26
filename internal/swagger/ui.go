package swagger

import (
	"net/http"

	httpSwagger "github.com/swaggo/http-swagger"
)

// darkThemeCSS keeps Swagger UI readable while matching the application's
// black interface. It is injected into the generated Swagger page so the
// theme stays applied when the API document is regenerated.
const darkThemeCSS = `
:root { color-scheme: dark; }
html, body, .swagger-ui, .swagger-ui .wrapper { background: #070909; }
body { margin: 0; }
.swagger-ui { color: #e7ece9; }
.swagger-ui .topbar { background: #050606; border-bottom: 1px solid #202522; }
.swagger-ui .topbar .download-url-wrapper input[type=text] {
  background: #111513; border-color: #52b72b; color: #f5f7f6;
}
.swagger-ui .topbar .download-url-wrapper .download-url-button { background: #52a633; }
.swagger-ui .info, .swagger-ui .scheme-container {
  background: #0b0e0d; color: #e7ece9;
}
.swagger-ui .info { margin: 0; padding: 48px 0; }
.swagger-ui .scheme-container {
  box-shadow: 0 1px 0 #252b28; margin: 0 0 28px; padding: 30px 0;
}
.swagger-ui .information-container.wrapper { background: #0b0e0d; max-width: none; padding-left: max(calc((100% - 1400px) / 2), 20px); padding-right: max(calc((100% - 1400px) / 2), 20px); }
.swagger-ui .wrapper { max-width: 1400px; }
.swagger-ui h1, .swagger-ui h2, .swagger-ui h3, .swagger-ui h4,
.swagger-ui h5, .swagger-ui p, .swagger-ui li, .swagger-ui label,
.swagger-ui .info .title, .swagger-ui .info .base-url,
.swagger-ui .opblock-tag, .swagger-ui .opblock-tag small,
.swagger-ui .opblock .opblock-summary-description,
.swagger-ui .opblock-description-wrapper p,
.swagger-ui .response-col_status, .swagger-ui .response-col_description,
.swagger-ui table thead tr td, .swagger-ui table thead tr th,
.swagger-ui .parameter__name, .swagger-ui .parameter__type,
.swagger-ui .model-title, .swagger-ui .model, .swagger-ui .prop-type,
.swagger-ui section.models h4, .swagger-ui section.models h5 { color: #e7ece9; }
.swagger-ui a, .swagger-ui .info a { color: #65baff; }
.swagger-ui .opblock-tag { border-bottom-color: #303734; }
.swagger-ui .opblock-tag:hover { background: #111513; }
.swagger-ui .opblock { background: #111513; box-shadow: none; }
.swagger-ui .opblock .opblock-section-header {
  background: #181d1b; box-shadow: 0 1px 2px rgba(0, 0, 0, .75);
}
.swagger-ui .opblock .opblock-section-header h4,
.swagger-ui .opblock .opblock-section-header label { color: #e7ece9; }
.swagger-ui .opblock.opblock-get { background: rgba(97, 175, 254, .09); }
.swagger-ui .opblock.opblock-post { background: rgba(73, 204, 144, .09); }
.swagger-ui .opblock.opblock-put { background: rgba(252, 161, 48, .09); }
.swagger-ui .opblock.opblock-delete { background: rgba(249, 62, 62, .09); }
.swagger-ui .opblock.opblock-patch { background: rgba(80, 227, 194, .09); }
.swagger-ui input[type=text], .swagger-ui input[type=password],
.swagger-ui input[type=email], .swagger-ui input[type=file],
.swagger-ui textarea, .swagger-ui select {
  background: #101412; border-color: #46504b; color: #f4f6f5;
}
.swagger-ui select { color-scheme: dark; }
.swagger-ui .btn { background: transparent; color: #e7ece9; }
.swagger-ui .btn.authorize { border-color: #49cc90; color: #49cc90; }
.swagger-ui .btn.authorize svg { fill: #49cc90; }
.swagger-ui .btn.execute { background: #3487d4; border-color: #3487d4; }
.swagger-ui .btn.cancel { border-color: #ef5555; color: #ff7474; }
.swagger-ui .highlight-code, .swagger-ui .microlight,
.swagger-ui .model-example, .swagger-ui .model-box,
.swagger-ui section.models { background: #0e1210; color: #e7ece9; }
.swagger-ui section.models { border-color: #303734; }
.swagger-ui section.models.is-open h4 { border-bottom-color: #303734; }
.swagger-ui .model-toggle:after, .swagger-ui .expand-operation,
.swagger-ui .opblock-control-arrow, .swagger-ui .models-control { filter: invert(1) brightness(1.5); }
.swagger-ui .dialog-ux .modal-ux { background: #111513; border-color: #363e3a; }
.swagger-ui .dialog-ux .modal-ux-header { border-bottom-color: #363e3a; }
.swagger-ui .dialog-ux .modal-ux-header h3 { color: #f2f5f3; }
.swagger-ui .dialog-ux .backdrop-ux { background: rgba(0, 0, 0, .82); }
.swagger-ui .loading-container .loading:after { color: #e7ece9; }
`

const darkThemeScript = `
const swaggerDarkTheme = document.createElement("style");
swaggerDarkTheme.id = "swagger-dark-theme";
swaggerDarkTheme.textContent = ` + "`" + darkThemeCSS + "`" + `;
document.head.appendChild(swaggerDarkTheme);
`

// UIHandler serves Swagger UI configured with the merged OpenAPI document and
// the application's dark theme.
func UIHandler() http.HandlerFunc {
	return httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
		httpSwagger.BeforeScript(darkThemeScript),
	)
}
