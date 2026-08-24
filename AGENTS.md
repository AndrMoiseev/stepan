# AGENTS.md

## Сборка и тесты

Требуется Go 1.26.5 и Windows/amd64. Все команды запускаются из корня репозитория.

```powershell
go build -o stepan.exe ./cmd/stepan
go test ./...
```

Для запуска тестов отдельного пакета используйте `go test ./путь/к/пакету`, например `go test ./internal/specflow`.
