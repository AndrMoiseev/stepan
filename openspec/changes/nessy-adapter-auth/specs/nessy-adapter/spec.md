## Purpose

Предоставить Nessy как самостоятельный провайдер Stepan с однозначным выбором CLI и идентичностью, не предполагающей совместимость с Qwen.

## ADDED Requirements

### Requirement: Nessy provider selection
Stepan SHALL принимать `--agent nessy` и разрешать только исполняемый файл `nessy` через PATH. Stepan SHALL отклонять параметр `--agent-cli-name` при выборе провайдера Nessy.

#### Scenario: Default Nessy executable
- **WHEN** пользователь запускает `stepan --agent nessy`
- **THEN** Stepan выбирает провайдер Nessy и исполняемый файл `nessy` из PATH.

#### Scenario: Executable override is rejected
- **WHEN** пользователь запускает `stepan --agent nessy --agent-cli-name corporate-nessy`
- **THEN** Stepan завершает запуск до создания процесса агента с ошибкой, объясняющей, что `--agent-cli-name` не поддерживается для Nessy.

#### Scenario: Redundant executable override is rejected
- **WHEN** пользователь запускает `stepan --agent nessy --agent-cli-name nessy`
- **THEN** Stepan отклоняет неподдерживаемый параметр, даже если его значение совпадает с фиксированным именем команды.

### Requirement: Qwen selection is removed
Stepan SHALL отклонять `--agent qwen` без совместимого алиаса и сообщать о необходимости использовать `--agent nessy`.

#### Scenario: Old invocation
- **WHEN** пользователь передаёт `--agent qwen`, в том числе вместе с `--agent-cli-name nessy`
- **THEN** запуск завершается ошибкой выбора провайдера с подсказкой перехода, до запуска агента.

### Requirement: Nessy identity and existing runtime behavior
Stepan SHALL обозначать выбранный провайдер как `nessy` в новых runtime-метаданных, диагностике и справке. Переименование SHALL сохранять существующий ACP-контракт структурированных ответов, ограничения инструментов и управление жизненным циклом процессов. Выбор Codex и Claude SHALL сохранять прежнее поведение.

#### Scenario: Nessy runtime identity
- **WHEN** Stepan создаёт runtime для `--agent nessy`
- **THEN** новые метаданные провайдера содержат `nessy`, а взаимодействие с агентом сохраняет действующие ограничения и контракт ответов.

#### Scenario: Other providers
- **WHEN** пользователь выбирает Codex или Claude
- **THEN** выбор и запуск этих провайдеров не меняются вследствие замены Qwen на Nessy.
