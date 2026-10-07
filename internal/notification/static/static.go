package static

import _ "embed"

// DefaultMailTemplate is the instance default MailTemplate.
//
//go:embed templates/template.html
var DefaultMailTemplate string
