# AGENTS.md

## Сборка и тесты

Требуется Go 1.26.5. Все команды запускаются из корня репозитория.

Поддерживаемая сборка для Windows/amd64:

```powershell
go build -o stepan.exe ./cmd/stepan
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

Бинарник компилируется, но запуск на macOS пока не поддерживается: runtime требует Windows/amd64.

Для запуска тестов отдельного пакета используйте `go test ./путь/к/пакету`, например `go test ./internal/specflow`.
