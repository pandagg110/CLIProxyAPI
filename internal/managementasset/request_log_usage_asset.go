package managementasset

import (
	"bytes"
	_ "embed"
)

// RequestLogUsageScriptPath is the same-origin path used by the management page.
const RequestLogUsageScriptPath = "/management-request-log-usage.js"

//go:embed request_log_usage.js
var requestLogUsageScript []byte

// RequestLogUsageScript returns an isolated copy of the embedded browser script.
func RequestLogUsageScript() []byte {
	return append([]byte(nil), requestLogUsageScript...)
}

// InjectRequestLogUsageScript adds the classic script at the start of the document head.
// Running before the control panel scripts ensures management requests can be observed.
func InjectRequestLogUsageScript(html []byte) []byte {
	if len(html) == 0 || bytes.Contains(html, []byte(RequestLogUsageScriptPath)) {
		return html
	}

	scriptTag := []byte(`<script src="` + RequestLogUsageScriptPath + `"></script>`)
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

func insertRequestLogUsageScript(html []byte, insertAt int, scriptTag []byte) []byte {
	out := make([]byte, 0, len(html)+len(scriptTag))
	out = append(out, html[:insertAt]...)
	out = append(out, scriptTag...)
	out = append(out, html[insertAt:]...)
	return out
}

func indexStartTagFoldASCII(value, name []byte) int {
	target := append([]byte{'<'}, name...)
	for index := 0; index+len(target) <= len(value); index++ {
		if !equalFoldASCII(value[index:index+len(target)], target) {
			continue
		}
		next := index + len(target)
		if next == len(value) {
			return index
		}
		switch value[next] {
		case ' ', '\t', '\r', '\n', '>', '/':
			return index
		}
	}
	return -1
}

func lastIndexFoldASCII(value, target []byte) int {
	for index := len(value) - len(target); index >= 0; index-- {
		if equalFoldASCII(value[index:index+len(target)], target) {
			return index
		}
	}
	return -1
}

func equalFoldASCII(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftByte := left[index]
		rightByte := right[index]
		if leftByte >= 'A' && leftByte <= 'Z' {
			leftByte += 'a' - 'A'
		}
		if rightByte >= 'A' && rightByte <= 'Z' {
			rightByte += 'a' - 'A'
		}
		if leftByte != rightByte {
			return false
		}
	}
	return true
}
