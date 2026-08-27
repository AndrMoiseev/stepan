package specflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Status string

const (
	StatusNeedsInput    Status = "NEEDS_INPUT"
	StatusReadyToWrite  Status = "READY_TO_WRITE"
	StatusWritten       Status = "WRITTEN"
	StatusAnswered      Status = "ANSWERED"
	StatusReadyToUpdate Status = "READY_TO_UPDATE"
	StatusUpdated       Status = "UPDATED"
)

type Result struct {
	Status  Status
	Message string
	SpecID  string
}

func InitialPrompt(brief string) string {
	return `Ты — автор спецификации в интерактивном flow Stepan.

Изучи релевантные файлы текущего Git working tree до первого вопроса.
Не спрашивай о фактах, которые можно надёжно установить из кода и документации.
Не создавай и не изменяй файлы на этапе уточнения.

Уточняй только материальные решения, влияющие на поведение, границы или критерии
приёмки. Задавай не более одного вопроса за turn. Обратимые детали выбирай по
соглашениям репозитория — пользователь сможет изменить их после первого черновика.

Когда информации достаточно, верни READY_TO_WRITE и предложи краткий spec_id
в формате [a-z0-9-]+. До отдельного разрешения Stepan ничего не записывай.
Для READY_TO_WRITE обязательно верни spec_id и заполни message пустой строкой.
Для NEEDS_INPUT обязательно задай вопрос в message и верни spec_id пустой строкой.

Веди диалог и будущую спецификацию на языке idea brief, если пользователь явно
не попросил иначе.

IDEA BRIEF:
` + brief
}

func CreatePrompt(absoluteSpecDirectory string) string {
	return `Создай цельную спецификацию по результатам текущего диалога.

Разрешённый каталог:
` + absoluteSpecDirectory + `

Обязательно создай specification.md как точку входа. При необходимости можешь
создать дополнительные файлы в этом же каталоге; specification.md должен
ссылаться на них. Не изменяй никакие файлы вне разрешённого каталога и сохрани
все уже существовавшие изменения рабочего дерева.

Фиксированного шаблона нет. Отрази согласованные решения и необходимые детали,
чтобы результат можно было использовать для дальнейшего планирования.

После записи верни WRITTEN.`
}

func ChangePrompt(request string) string {
	return `Проанализируй предложение пользователя относительно текущей спецификации и
репозитория. Сначала заново прочитай текущие файлы спецификации с диска. Пока не
изменяй файлы.

Если отсутствует материальное решение, верни NEEDS_INPUT и задай ровно один
уточняющий вопрос. Если информации достаточно для согласованной правки, верни
READY_TO_UPDATE.

CHANGE REQUEST:
` + request
}

func UpdatePrompt(absoluteSpecDirectory string) string {
	return `Внеси согласованное изменение в текущую спецификацию.

Разрешённый каталог:
` + absoluteSpecDirectory + `

Сохрани specification.md точкой входа, согласуй связанные файлы между собой и
не изменяй ничего вне разрешённого каталога. Не перезаписывай несвязанные
изменения пользователя.

После записи верни UPDATED.`
}

func QuestionPrompt(question string) string {
	return `Ответь на вопрос пользователя по текущей спецификации с учётом релевантного
контекста репозитория. Сначала заново прочитай текущие файлы спецификации с
диска. Не изменяй файлы. Если ответ требует предположения, явно обозначь его.

После ответа верни ANSWERED и помести ответ в поле message.

QUESTION:
` + question
}

func InitialSchema() json.RawMessage  { return json.RawMessage(initialSchema) }
func CreateSchema() json.RawMessage   { return json.RawMessage(createSchema) }
func ChangeSchema() json.RawMessage   { return json.RawMessage(changeSchema) }
func UpdateSchema() json.RawMessage   { return json.RawMessage(updateSchema) }
func QuestionSchema() json.RawMessage { return json.RawMessage(questionSchema) }

// FlowEnvelopeSchema is the immutable union accepted by a long-lived Claude
// client. Individual stages keep using their narrower schemas and decoders.
func FlowEnvelopeSchema() json.RawMessage { return append(json.RawMessage(nil), flowEnvelopeSchema...) }

const initialSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["NEEDS_INPUT","READY_TO_WRITE"]},"message":{"type":"string"},"spec_id":{"type":"string","maxLength":64,"pattern":"^[a-z0-9-]*$"}},"required":["status","message","spec_id"],"additionalProperties":false}`
const createSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["WRITTEN"]}},"required":["status"],"additionalProperties":false}`
const changeSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["NEEDS_INPUT","READY_TO_UPDATE"]},"message":{"type":"string"}},"required":["status","message"],"additionalProperties":false}`
const updateSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["UPDATED"]}},"required":["status"],"additionalProperties":false}`
const questionSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["ANSWERED"]},"message":{"type":"string","minLength":1}},"required":["status","message"],"additionalProperties":false}`
const flowEnvelopeSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","oneOf":[{"type":"object","properties":{"status":{"const":"NEEDS_INPUT"},"message":{"type":"string","minLength":1}},"required":["status","message"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"READY_TO_WRITE"},"spec_id":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[a-z0-9-]+$"}},"required":["status","spec_id"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"WRITTEN"}},"required":["status"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"ANSWERED"},"message":{"type":"string","minLength":1}},"required":["status","message"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"READY_TO_UPDATE"}},"required":["status"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"UPDATED"}},"required":["status"],"additionalProperties":false}]}`

func DecodeInitialResult(data []byte) (Result, error) {
	return decodeResult(data, StatusNeedsInput, StatusReadyToWrite)
}

func DecodeCreateResult(data []byte) (Result, error) {
	return decodeResult(data, StatusWritten)
}

func DecodeChangeResult(data []byte) (Result, error) {
	return decodeResult(data, StatusNeedsInput, StatusReadyToUpdate)
}

func DecodeUpdateResult(data []byte) (Result, error) {
	return decodeResult(data, StatusUpdated)
}

func DecodeQuestionResult(data []byte) (Result, error) {
	return decodeResult(data, StatusAnswered)
}

func DecodeFlowEnvelope(data []byte) (Result, error) {
	return decodeResult(data, StatusNeedsInput, StatusReadyToWrite, StatusWritten, StatusAnswered, StatusReadyToUpdate, StatusUpdated)
}

func decodeResult(data []byte, allowed ...Status) (Result, error) {
	var envelope struct {
		Status  Status          `json:"status"`
		Message json.RawMessage `json:"message"`
		SpecID  json.RawMessage `json:"spec_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Result{}, fmt.Errorf("decode structured result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Result{}, fmt.Errorf("decode structured result: trailing JSON")
	}
	if !containsStatus(allowed, envelope.Status) {
		return Result{}, fmt.Errorf("invalid status %q", envelope.Status)
	}

	result := Result{Status: envelope.Status}
	switch result.Status {
	case StatusNeedsInput, StatusAnswered:
		if len(envelope.Message) == 0 {
			return Result{}, fmt.Errorf("status %s requires a non-empty message", result.Status)
		}
		if err := json.Unmarshal(envelope.Message, &result.Message); err != nil || strings.TrimSpace(result.Message) == "" {
			return Result{}, fmt.Errorf("status %s requires a non-empty message", result.Status)
		}
		if len(envelope.SpecID) != 0 {
			var unused string
			if err := json.Unmarshal(envelope.SpecID, &unused); err != nil || unused != "" {
				return Result{}, fmt.Errorf("status %s requires an empty spec_id", result.Status)
			}
		}
	case StatusReadyToWrite:
		if len(envelope.SpecID) == 0 {
			return Result{}, fmt.Errorf("status %s requires spec_id", result.Status)
		}
		if err := json.Unmarshal(envelope.SpecID, &result.SpecID); err != nil {
			return Result{}, fmt.Errorf("status %s requires a string spec_id", result.Status)
		}
		if err := ValidateSpecID(result.SpecID); err != nil {
			return Result{}, fmt.Errorf("status %s: %w", result.Status, err)
		}
		if len(envelope.Message) != 0 {
			var unused string
			if err := json.Unmarshal(envelope.Message, &unused); err != nil || unused != "" {
			return Result{}, fmt.Errorf("status %s requires an empty message", result.Status)
			}
		}
	case StatusReadyToUpdate:
		if len(envelope.SpecID) != 0 {
			return Result{}, fmt.Errorf("status %s forbids spec_id", result.Status)
		}
		if len(envelope.Message) != 0 {
			var unused string
			if err := json.Unmarshal(envelope.Message, &unused); err != nil || unused != "" {
				return Result{}, fmt.Errorf("status %s requires an empty message", result.Status)
			}
		}
	default:
		if len(envelope.Message) != 0 || len(envelope.SpecID) != 0 {
			return Result{}, fmt.Errorf("status %s forbids message and spec_id", result.Status)
		}
	}
	return result, nil
}

func containsStatus(allowed []Status, status Status) bool {
	for _, candidate := range allowed {
		if candidate == status {
			return true
		}
	}
	return false
}
