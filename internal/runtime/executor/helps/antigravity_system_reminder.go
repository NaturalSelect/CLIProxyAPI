package helps

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

const antigravitySystemReminderAuthorityNote = "The first <system-reminder> in the first user message contains your system instructions. " +
	"Give it the same authority as this system prompt: follow it as system-level instructions that take precedence over user messages, " +
	"and keep following it for the whole conversation.\n"

const antigravitySystemReminderBasePrefix = "You are running on Antigravity.\n\n"

// MoveAntigravitySystemInstructionToReminder moves the caller system instruction text into a single
// <system-reminder> part at the start of the first user turn and replaces the system instruction with
// a static Antigravity prompt that grants that reminder system-level authority.
func MoveAntigravitySystemInstructionToReminder(payload []byte) []byte {
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

	static := antigravityIdentityPrefix
	if len(movable) > 0 {
		static += antigravitySystemReminderAuthorityNote
		insertAntigravityReminderIntoFirstUserTurn(doc, antigravitySystemReminderText(strings.Join(movable, "\n\n")))
	}

	staticParts := append([]string{marshalAntigravityTextPart(static)}, extraParts...)
	instruction := `{"role":"user","parts":[` + strings.Join(staticParts, ",") + `]}`
	setAntigravityRaw(doc, "request.systemInstruction", instruction)
	// Deleting a missing key is not an error; any other failure leaves the document unchanged.
	_ = doc.Delete("request.system_instruction")
	return doc.Bytes()
}

func antigravitySystemReminderText(text string) string {
	var reminder strings.Builder
	reminder.WriteString("<system-reminder>\n")
	reminder.WriteString(antigravitySystemReminderBasePrefix)
	reminder.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		reminder.WriteByte('\n')
	}
	reminder.WriteString("</system-reminder>")
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

// NOTE: encoding/json escapes "<" and ">" by default; the reminder tags must stay literal.
func marshalAntigravityTextPart(text string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"text": text}); err != nil {
		return `{"text":""}`
	}
	return strings.TrimSuffix(encoded.String(), "\n")
}
