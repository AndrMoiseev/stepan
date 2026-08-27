# Шаблоны prompt'ов specflow

Каждый turn собирается из system-шаблона и, если есть пользовательский вход,
user-шаблона. System-файлы определяют протокол, допустимые изменения и формат
ответа; user-файлы передают только артефакт пользователя.

| Turn | System | User | Переменные user-шаблона |
| --- | --- | --- | --- |
| Первый черновик | `initial-system.md` | `initial-user.md` | `{{brief}}` |
| Изменение | `change-system.md` | `change-user.md` | `{{change_request}}` |
| Уточнение изменения | `change-answer-system.md` | `change-answer-user.md` | `{{answer}}` |
| Вопрос | `question-system.md` | `question-user.md` | `{{question}}` |

`initial-system.md` также получает `{{features_directory}}`, а
`update-system.md` — `{{spec_directory}}`. Шаблоны встроены в бинарник через
`go:embed`; проект может менять их как часть своей конфигурации и пересобрать
Stepan, не меняя логику flow.
