package web

import (
	"bytes"
	"html/template"
	"strings"
)

func applicationShell(name, html string) (string, error) {
	routes := map[string]string{"dashboard.html": "/", "profile.html": "/profile", "opportunities.html": "/opportunities", "opportunity.html": "/opportunities", "companies.html": "/companies", "company.html": "/companies", "plan.html": "/companies", "compositions.html": "/compositions", "composition.html": "/compositions", "threads.html": "/threads", "thread.html": "/threads", "legacy_drafts.html": "/legacy/drafts", "legacy_threads.html": "/legacy/threads", "templates_admin.html": "/templates", "drafts.html": "/compositions", "sent.html": "/legacy/threads"}

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
		return strings.Replace(html, `<body class="prospector-dashboard">`, `<body class="prospector-dashboard">`+sidebar.String(), 1), nil
	}
	html = strings.Replace(html, "</head>", `<meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/static/dashboard.css"><link rel="stylesheet" href="/static/shell.css"></head>`, 1)
	html = strings.Replace(html, "<body>", `<body class="prospector-page"><a class="skip-link" href="#page-main">Skip to content</a>`+sidebar.String()+`<main id="page-main" class="application-main">`, 1)
	html = strings.Replace(html, "</body>", "</main></body>", 1)
	return html, nil
}
