# AGENTS.md

## Глубокие модули и вложенные пакеты

При проектировании нового модуля стремитесь к небольшому интерфейсу, скрывающему связное поведение. Модуль, обслуживающий один flow, по умолчанию размещайте внутри его каталога; место уточняйте по фактическим потребителям и направлению зависимостей. В названии вложенного пакета обозначайте роль без повторения имени родителя. Если вложенный пакет нужен хранилищу или точке сборки, зависимость направляйте на этот пакет, а не на пакет flow. Правила импортов обновляйте в `arch-go.yml`.

## Сборка и тесты

Требуется Go 1.26.5. Все команды запускаются из корня репозитория.

Поддерживаемая сборка для Windows/amd64:

```powershell
go build -o stepan.exe ./cmd/stepan
go test ./...
```

Нативная сборка на Apple Silicon Mac:

```sh
go build -o stepan ./cmd/stepan
go test ./...
```

Кросс-сборка для macOS на Apple Silicon:

```powershell
$env:GOOS = "darwin"
$env:GOARCH = "arm64"
$env:CGO_ENABLED = "0"
go build -o stepan-darwin-arm64 ./cmd/stepan
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Cross-build проверяет только компиляцию. Runtime-приёмку macOS выполняйте на
физическом Apple Silicon Mac по
`docs/changes/features/macos-compatibility/manual-test-plan.md`. Intel Mac, signing и
notarization пока не поддерживаются.

Для запуска тестов отдельного пакета используйте `go test ./путь/к/пакету`, например `go test ./internal/specflow`.

## Наборы тестов

Обычный запуск выполняет только быстрые изолированные тесты и не должен
запускать внешние процессы:

```powershell
go test ./...
```

Контракты с настоящим Git запускаются отдельно:

```powershell
go test -parallel=4 -tags=git_integration ./...
```

Тесты управления дочерними процессами и fake CLI запускаются отдельно:

```powershell
go test -tags=process_integration ./...
```

Полная локальная проверка выполняет оба интеграционных набора:

```powershell
go test -parallel=4 -tags=git_integration,process_integration ./...
```

Файлы тестов, импортирующие `os/exec`, обязаны иметь build tag
`git_integration`, `process_integration` или специальный ручной tag вроде
`nessy_real_cli`. Это правило проверяется архитектурным тестом.

## Codex App Server: response schema

Для `turn/start` schema в `text.format.schema` используйте flat object-schema:

- `oneOf` не поддерживается;
- `required` должен содержать каждый ключ из `properties`;
- для семантически необязательного поля используйте required transport-placeholder
  и нормализуйте его до передачи в доменный слой. Например, intent `draft`
  передаёт `"message": ""`; пустое значение не является сообщением draft.

## GitHub Actions

Для локальной проверки всех workflow используйте закреплённую версию
`actionlint`:

```text
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```
