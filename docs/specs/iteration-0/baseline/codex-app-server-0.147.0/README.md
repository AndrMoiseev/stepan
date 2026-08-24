# Codex App Server 0.147.0 baseline

Baseline зафиксирован 2026-08-10 на Windows native для `codex-cli 0.147.0`.
Schema bundle сгенерирован установленным CLI без `--experimental`, как требует
[спецификация](../../specification.md). Официальная документация подтверждает,
что generated schema привязана к конкретной версии Codex:
[Codex App Server](https://learn.chatgpt.com/docs/app-server#message-schema).

В репозитории сохранены:

- санитизированный stdout `codex app-server --help` в
  [app-server-help.txt](app-server-help.txt);
- SHA-256 каждого файла generated bundle в
  [schema-bundle.sha256](schema-bundle.sha256).

Полный bundle остаётся live-артефактом в `.stepan/spike/` и не коммитится. Он
содержит 285 файлов общим размером 2 925 973 байта. SHA-256 файла
`schema-bundle.sha256` при UTF-8 без BOM и LF:

```text
eb325d394d19f2f8d133203885b3d1c2f74dbc5a176f22078a4f99aae5926faa
```

Основные bundle-файлы:

| Файл | SHA-256 |
| --- | --- |
| `codex_app_server_protocol.schemas.json` | `f72b2caa3cbfa4298de9e85c62dda6dfbaf2266ffeb916fed30615ca69ff8c74` |
| `codex_app_server_protocol.v2.schemas.json` | `f3dec1e031d99a420b137b903f02196d4325eece57620c925bb7130b25f168d2` |

## Воспроизведение

Команды выполняются из корня репозитория в PowerShell. Каталог назначения
должен отсутствовать: CLI создаёт его сам.

```powershell
codex --version
codex app-server --help

$out = '.stepan/spike/appserver-schema-0.147.0-repro'
if (Test-Path -LiteralPath $out) {
    throw "Refusing to overwrite existing evidence: $out"
}
codex app-server generate-json-schema --out $out
if ($LASTEXITCODE -ne 0) {
    throw "Schema generation failed: exit $LASTEXITCODE"
}

$root = (Resolve-Path $out).Path
$paths = [System.Collections.Generic.List[string]]::new()
Get-ChildItem -Recurse -File $root | ForEach-Object {
    $paths.Add($_.FullName.Substring($root.Length + 1).Replace('\', '/'))
}
$paths.Sort([System.StringComparer]::Ordinal)

$lines = foreach ($path in $paths) {
    $file = Join-Path $root $path.Replace('/', '\')
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash.ToLowerInvariant()
    "$hash  $path"
}
$text = ($lines -join "`n") + "`n"
$encoding = [System.Text.UTF8Encoding]::new($false)
$manifest = Join-Path $out 'schema-bundle.sha256'
[System.IO.File]::WriteAllText($manifest, $text, $encoding)
(Get-FileHash -Algorithm SHA256 -LiteralPath $manifest).Hash.ToLowerInvariant()
```

Полученный manifest должен совпасть с committed `schema-bundle.sha256`, а его
SHA-256 — со значением выше. В baseline не включены stderr и абсолютный путь к
`codex.exe`: sandbox этой среды добавляет в stderr локальные диагностические
пути, не относящиеся к protocol contract.
