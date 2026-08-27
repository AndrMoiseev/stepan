# AGENTS.md

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

## GitHub Actions

Для локальной проверки всех workflow используйте закреплённую версию
`actionlint`:

```text
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```
