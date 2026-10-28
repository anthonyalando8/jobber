package web

import (
	"bytes"
	"html/template"
	"strings"
)

func applicationShell(name, html string) (string, error) {
	routes := map[string]string{"dashboard.html": "/", "search.html": "/search", "messages.html": "/messages", "profile.html": "/profile", "opportunities.html": "/opportunities", "opportunity.html": "/opportunities", "companies.html": "/companies", "company.html": "/companies", "plan.html": "/companies", "contact_discovery.html": "/companies", "compositions.html": "/compositions", "composition.html": "/compositions", "threads.html": "/threads", "thread.html": "/threads", "legacy_drafts.html": "/legacy/drafts", "legacy_threads.html": "/legacy/threads", "templates_admin.html": "/templates", "drafts.html": "/legacy/drafts", "review.html": "/legacy/drafts", "sent.html": "/legacy/threads", "followups.html": "/legacy/threads", "sources.html": "/sources", "held.html": "/sources"}

	active, ok := routes[name]
	if !ok {
		return html, nil
	}
	t, err := template.ParseFiles("internal/web/templates/sidebar.html")
	if err != nil {
		return "", err
	}
	var sidebar bytes.Buffer
	if err = t.Execute(&sidebar, map[string]string{"Active": active}); err != nil {
		return "", err
	}
	if name == "dashboard.html" {
		return strings.Replace(html, `<body class="prospector-dashboard">`, `<body class="prospector-dashboard prospector-page">`+sidebar.String(), 1), nil
	}
	html = strings.Replace(html, "</head>", `<meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/static/dashboard.css?v=4"><link rel="stylesheet" href="/static/shell.css?v=7"><link rel="stylesheet" href="/static/lists.css?v=2"></head>`, 1)
	html = strings.Replace(html, "<body>", `<body class="prospector-page"><a class="skip-link" href="#page-main">Skip to content</a>`+sidebar.String()+`<main id="page-main" class="application-main">`, 1)
	html = strings.Replace(html, "</body>", "</main></body>", 1)
	return html, nil
}
