package helps

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NOTE: Verbatim stock ephemeral_message.tmpl (271 bytes), wrapped in its tag like the identity prompt.
const antigravityEphemeralMessageNote = "<ephemeral_message>\n" +
	"There will be an <EPHEMERAL_MESSAGE> appearing in the conversation at times. This is not coming from the user, but instead injected by the system as important information to pay attention to. \n" +
	"Do not respond to nor acknowledge those messages, but do follow them strictly.\n" +
	"</ephemeral_message>\n"

const AntigravitySystemReminderBasePrefix = "To be precise, you are hosted on the Antigravity platform. " +
	"The following messages are wrapped by <USER_REQUEST>, including the system message.\n\n"

const (
	antigravityUserRequestTag   = "<USER_REQUEST>"
	antigravityUserRequestOpen  = antigravityUserRequestTag + "\n"
	antigravityUserRequestClose = "\n</USER_REQUEST>"
)

// MoveAntigravitySystemInstructionToReminder moves the caller system instruction text into a single
// <EPHEMERAL_MESSAGE> part at the start of the first user turn and replaces the system instruction with
// the static Antigravity prompt that tells the model to follow such messages strictly. Real user text is
// wrapped in <USER_REQUEST> when wrapUserRequests is set, because the static prompt tells the model that
// user requests come that way.
func MoveAntigravitySystemInstructionToReminder(payload []byte, wrapUserRequests bool) []byte {
	doc := ojson.NewDoc(payload)
	var texts, extraParts []string
	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		instruction := doc.Get(path)
		if !instruction.Exists() {
			continue
		}
		if instruction.Type == ojson.String {
			texts = append(texts, instruction.String())
			continue
		}
		instruction.Get("parts").ForEach(func(_, part ojson.Result) bool {
			if part.Get("text").Type == ojson.String {
				texts = append(texts, part.Get("text").String())
			} else {
				extraParts = append(extraParts, part.Raw)
			}
			return true
		})
	}

	movable := make([]string, 0, len(texts))
	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			movable = append(movable, text)
		}
	}

	if wrapUserRequests {
		wrapAntigravityUserRequests(doc)
	}

	static := antigravityIdentityPrefix
	if len(movable) > 0 {
		static += antigravityEphemeralMessageNote
		insertAntigravityReminderIntoFirstUserTurn(doc, antigravitySystemReminderText(strings.Join(movable, "\n\n")))
	}

	staticParts := append([]string{marshalAntigravityTextPart(static)}, extraParts...)
	instruction := `{"role":"user","parts":[` + strings.Join(staticParts, ",") + `]}`
	setAntigravityRaw(doc, "request.systemInstruction", instruction)
	// Deleting a missing key is not an error; any other failure leaves the document unchanged.
	_ = doc.Delete("request.system_instruction")
	return doc.Bytes()
}

// NOTE: The native Antigravity client already wraps its own user text, so its requests must not be wrapped again.
func IsAntigravityClientUserAgent(userAgent string) bool {
	const prefix = "antigravity"
	value := strings.TrimSpace(userAgent)
	return len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}

func UnwrapAntigravityUserRequest(text string) string {
	rest, ok := strings.CutPrefix(text, antigravityUserRequestOpen)
	if !ok {
		return text
	}
	return strings.TrimSuffix(rest, antigravityUserRequestClose)
}

// NOTE: One envelope per user turn spans its first to last real text part, so images and files stay inside it.
func wrapAntigravityUserRequests(doc *ojson.Doc) {
	contents := doc.Get("request.contents")
	if !contents.IsArray() {
		return
	}

	type textEdit struct{ path, text string }
	var edits []textEdit
	for idx, turn := range contents.Array() {
		parts := turn.Get("parts")
		if turn.Get("role").String() != "user" || !parts.IsArray() {
			continue
		}
		items := parts.Array()
		first, last := -1, -1
		for partIdx, item := range items {
			if !isAntigravityUserRequestText(item.Get("text")) {
				continue
			}
			if first < 0 {
				first = partIdx
			}
			last = partIdx
		}
		if first < 0 {
			continue
		}

		base := "request.contents." + strconv.Itoa(idx) + ".parts."
		firstText := items[first].Get("text").String()
		if first == last {
			edits = append(edits, textEdit{base + strconv.Itoa(first) + ".text", antigravityUserRequestOpen + firstText + antigravityUserRequestClose})
			continue
		}
		edits = append(edits,
			textEdit{base + strconv.Itoa(first) + ".text", antigravityUserRequestOpen + firstText},
			textEdit{base + strconv.Itoa(last) + ".text", items[last].Get("text").String() + antigravityUserRequestClose},
		)
	}
	for _, edit := range edits {
		setAntigravityRaw(doc, edit.path, marshalAntigravityString(edit.text))
	}
}

func isAntigravityUserRequestText(text ojson.Result) bool {
	if text.Type != ojson.String {
		return false
	}
	value := text.String()
	if strings.TrimSpace(value) == "" {
		return false
	}
	return !strings.HasPrefix(value, "<system-reminder>") &&
		!strings.HasPrefix(value, "<EPHEMERAL_MESSAGE>") &&
		!strings.HasPrefix(value, antigravityUserRequestTag)
}

func antigravitySystemReminderText(text string) string {
	var reminder strings.Builder
	reminder.WriteString("<EPHEMERAL_MESSAGE>\n")
	reminder.WriteString(AntigravitySystemReminderBasePrefix)
	reminder.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		reminder.WriteByte('\n')
	}
	reminder.WriteString("</EPHEMERAL_MESSAGE>")
	return reminder.String()
}

func insertAntigravityReminderIntoFirstUserTurn(doc *ojson.Doc, reminder string) {
	reminderPart := marshalAntigravityTextPart(reminder)
	contents := doc.Get("request.contents")
	if !contents.IsArray() {
		setAntigravityRaw(doc, "request.contents", `[{"role":"user","parts":[`+reminderPart+`]}]`)
		return
	}

	turns := contents.Array()
	for idx, turn := range turns {
		if turn.Get("role").String() != "user" {
			continue
		}
		parts := turn.Get("parts")
		partsPath := "request.contents." + strconv.Itoa(idx) + ".parts"
		if !parts.IsArray() {
			setAntigravityRaw(doc, partsPath, `[`+reminderPart+`]`)
			return
		}
		items := parts.Array()
		insertAt := 0
		for insertAt < len(items) && (items[insertAt].Get("functionResponse").Exists() || items[insertAt].Get("function_response").Exists()) {
			insertAt++
		}
		raw := make([]string, 0, len(items)+1)
		for i, item := range items {
			if i == insertAt {
				raw = append(raw, reminderPart)
			}
			raw = append(raw, item.Raw)
		}
		if insertAt == len(items) {
			raw = append(raw, reminderPart)
		}
		setAntigravityRaw(doc, partsPath, "["+strings.Join(raw, ",")+"]")
		return
	}

	raw := make([]string, 0, len(turns)+1)
	raw = append(raw, `{"role":"user","parts":[`+reminderPart+`]}`)
	for _, turn := range turns {
		raw = append(raw, turn.Raw)
	}
	setAntigravityRaw(doc, "request.contents", "["+strings.Join(raw, ",")+"]")
}

// setAntigravityRaw sets a raw JSON value and leaves the document unchanged on failure.
func setAntigravityRaw(doc *ojson.Doc, path, raw string) {
	_ = doc.SetRaw(path, raw)
}

// NOTE: encoding/json escapes "<" and ">" by default; the injected tags must stay literal.
func marshalAntigravityString(text string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(text); err != nil {
		return `""`
	}
	return strings.TrimSuffix(encoded.String(), "\n")
}

func marshalAntigravityTextPart(text string) string {
	return `{"text":` + marshalAntigravityString(text) + `}`
}
