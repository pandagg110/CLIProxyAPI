package managementasset

import (
	"bytes"
	_ "embed"
)

// LogQAScriptPath is the same-origin path used by the management page.
const LogQAScriptPath = "/management-log-qa.js"

//go:embed log_qa.js
var logQAScript []byte

// LogQAScript returns an isolated copy of the embedded browser script.
func LogQAScript() []byte {
	return append([]byte(nil), logQAScript...)
}

// InjectLogQAScript adds the classic script near the start of the document head.
func InjectLogQAScript(html []byte) []byte {
	if len(html) == 0 || bytes.Contains(html, []byte(LogQAScriptPath)) {
		return html
	}

	scriptTag := []byte(`<script src="` + LogQAScriptPath + `"></script>`)
	if headStart := indexStartTagFoldASCII(html, []byte("head")); headStart >= 0 {
		if relativeEnd := bytes.IndexByte(html[headStart:], '>'); relativeEnd >= 0 {
			return insertRequestLogUsageScript(html, headStart+relativeEnd+1, scriptTag)
		}
	}
	if firstScript := indexStartTagFoldASCII(html, []byte("script")); firstScript >= 0 {
		return insertRequestLogUsageScript(html, firstScript, scriptTag)
	}

	closingBody := []byte("</body>")
	insertAt := lastIndexFoldASCII(html, closingBody)
	if insertAt < 0 {
		out := make([]byte, 0, len(html)+len(scriptTag))
		out = append(out, html...)
		out = append(out, scriptTag...)
		return out
	}
	return insertRequestLogUsageScript(html, insertAt, scriptTag)
}
