## Purpose

Обеспечить неинтерактивную авторизацию процессов Nessy готовым токеном, единственным источником которого служит пользовательский файл конфигурации Stepan.

## ADDED Requirements

### Requirement: User configuration is the sole token source
Для провайдера Nessy Stepan SHALL читать токен исключительно из строкового поля `nessy.auth_token` JSON-файла `.stepan/settings.json` в домашнем каталоге текущего пользователя (`~/.stepan/settings.json`). Проектный каталог и унаследованная переменная `NESSY_CLI_DP_AUTH_TOKEN` SHALL NOT служить альтернативными источниками токена.

Пример структуры:

```json
{
  "nessy": {
    "auth_token": "user-provided-token"
  }
}
```

#### Scenario: Token configured
- **WHEN** пользователь выбирает Nessy и домашний файл конфигурации содержит непустую строку `nessy.auth_token`
- **THEN** Stepan использует это значение как готовый токен для Nessy независимо от текущего рабочего каталога.

#### Scenario: Environment cannot replace configuration
- **WHEN** в конфигурации нет токена, но в окружении задана `NESSY_CLI_DP_AUTH_TOKEN`
- **THEN** Stepan сообщает об отсутствующем токене конфигурации и не запускает Nessy.

### Requirement: Configuration errors prevent Nessy startup
При отсутствии файла, невозможности его прочитать, невалидном JSON, отсутствии поля, нестроковом значении или строке, пустой либо состоящей только из пробелов, Stepan SHALL завершать запуск Nessy с понятной ошибкой, указывающей на `~/.stepan/settings.json` и требуемое поле. Stepan SHALL NOT переключаться на интерактивную авторизацию. Настройки Nessy SHALL NOT быть обязательными для других провайдеров.

#### Scenario: Invalid configuration
- **WHEN** конфигурация Nessy отсутствует, недоступна для чтения или не удовлетворяет требованиям формата и наличия токена
- **THEN** Stepan завершает запуск до создания процесса Nessy и сообщает причину без вывода содержимого файла.

#### Scenario: Other provider without Nessy settings
- **WHEN** пользователь выбирает Codex или Claude, а настройки Nessy отсутствуют или некорректны
- **THEN** настройки Nessy не препятствуют запуску выбранного провайдера.

### Requirement: Configured token overrides child environment
Stepan SHALL передавать значение `nessy.auth_token` через переменную `NESSY_CLI_DP_AUTH_TOKEN` каждому запускаемому им процессу агента Nessy, включая процессы отдельных потоков. Унаследованное значение этой переменной SHALL быть заменено значением из файла. Токен SHALL NOT передаваться аргументом командной строки. Контракт интеграции исходит из того, что Nessy при наличии этой переменной не использует интерактивную аутентификацию.

#### Scenario: Conflicting environment value
- **WHEN** файл содержит токен A, а окружение Stepan содержит токен B в `NESSY_CLI_DP_AUTH_TOKEN`
- **THEN** каждый запускаемый процесс Nessy получает только токен A как значение этой переменной.

#### Scenario: Multiple agent processes
- **WHEN** Stepan запускает несколько процессов Nessy в рамках работы агентов
- **THEN** каждый процесс получает настроенный токен через окружение без дополнительного интерактивного входа.

### Requirement: Stepan does not obtain or refresh credentials
Stepan SHALL использовать настроенный токен непосредственно, без HTTP-обмена, автоматического обновления JWT, вызовов `dp auth login`, `dp auth print-token` или предварительного интерактивного запуска Nessy. Получение и замена токена SHALL оставаться ответственностью пользователя.

#### Scenario: Noninteractive startup
- **WHEN** настроенный токен доступен и пользователь запускает Nessy через Stepan
- **THEN** Stepan передаёт токен процессу агента без запуска вспомогательных команд авторизации и без запросов к endpoint обмена токенов.

### Requirement: Token confidentiality
Stepan SHALL NOT включать токен в пользовательские сообщения, логи, промпты или сохраняемые артефакты работы. Диагностика дочернего процесса SHALL скрывать значение настроенного токена, если оно присутствует в выводе.

#### Scenario: Child diagnostic contains token
- **WHEN** Nessy выводит настроенный токен в диагностическом сообщении, которое обрабатывает Stepan
- **THEN** диагностический вывод Stepan не раскрывает значение токена.
