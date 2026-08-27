# Stepan: ручное тестирование совместимости с macOS

Статус: выполнять после реализации `MAC-01`–`MAC-05`

Основной стенд: физический Apple Silicon Mac, macOS/arm64

Связанный документ: [план реализации](implementation-plan.md)

## 1. Как пользоваться планом

Тесты выполняются по порядку. Для каждого сценария нужно поставить один статус:

- `PASS` — все ожидаемые результаты получены;
- `FAIL` — результат отличается от ожидаемого;
- `BLOCKED` — сценарий нельзя выполнить, с указанием причины.

При `FAIL` сохраните команду, полный текст ошибки и снимок списка процессов.
Не продолжайте выпуск при `FAIL` в обязательном сценарии. Сценарии `MAC-M01`–
`MAC-M10` и `WIN-M01` обязательны.

## 2. Подготовка стенда

Понадобятся:

- Apple Silicon Mac и обычный интерактивный Terminal или iTerm2;
- Go `1.26.5`;
- Git;
- установленный, авторизованный `codex-cli 0.147.0`;
- две вкладки терминала: A для Stepan, B для наблюдения за процессами;
- checkout Stepan с реализованной macOS-совместимостью.

Сначала зафиксируйте окружение:

```sh
sw_vers
uname -s
uname -m
go version
git --version
codex --version
git -C /absolute/path/to/stepan rev-parse HEAD
git -C /absolute/path/to/stepan status --short
```

Ожидается `Darwin`, `arm64`, Go `1.26.5` и `codex-cli 0.147.0`. Если версия
Codex отличается, остановите основные сценарии и отметьте их `BLOCKED`: менять
version pin в рамках этой приёмки нельзя.

В терминале A создайте изолированный стенд. Замените путь к исходникам:

```sh
export STEPAN_SOURCE="/absolute/path/to/stepan"
export STEPAN_TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/stepan-macos.XXXXXX")"
export STEPAN_BIN="$STEPAN_TEST_ROOT/stepan"
export STEPAN_REPO="$STEPAN_TEST_ROOT/workspace"
touch "$STEPAN_TEST_ROOT/.stepan-manual-test-root"

cd "$STEPAN_SOURCE"
go build -o "$STEPAN_BIN" ./cmd/stepan

git init "$STEPAN_REPO"
git -C "$STEPAN_REPO" config user.name "Stepan Manual Test"
git -C "$STEPAN_REPO" config user.email "stepan-manual@example.invalid"
printf '%s\n' '# macOS manual test workspace' > "$STEPAN_REPO/README.md"
git -C "$STEPAN_REPO" add README.md
git -C "$STEPAN_REPO" commit -m "initial test workspace"

printf 'STEPAN_TEST_ROOT=%s\n' "$STEPAN_TEST_ROOT"
```

Скопируйте напечатанное значение `STEPAN_TEST_ROOT` в терминал B и выполните:

```sh
export STEPAN_TEST_ROOT="/printed/path/from/terminal-a"
export STEPAN_BIN="$STEPAN_TEST_ROOT/stepan"
export STEPAN_REPO="$STEPAN_TEST_ROOT/workspace"
```

Все создаваемые спецификации находятся во временном Git-репозитории, а не в
checkout исходников Stepan.

## 3. Сценарии macOS

### MAC-M01. Нативная сборка

В любом терминале:

```sh
file "$STEPAN_BIN"
test -x "$STEPAN_BIN"
printf 'exit=%s\n' "$?"
```

Ожидается:

- `file` сообщает `Mach-O 64-bit executable arm64`;
- binary исполняемый;
- итоговый `exit=0`.

Evidence: вывод `file` и размер binary (`ls -lh "$STEPAN_BIN"`).

### MAC-M02. Проверка интерактивного терминала

Из тестового Git-репозитория проверьте redirected stdin:

```sh
cd "$STEPAN_REPO"
"$STEPAN_BIN" </dev/null
printf 'exit=%s\n' "$?"
```

Затем redirected stdout:

```sh
"$STEPAN_BIN" > "$STEPAN_TEST_ROOT/redirected-stdout.txt"
printf 'exit=%s\n' "$?"
```

Ожидается код `2` и понятная ошибка о том, что соответствующий поток должен быть
терминалом. Сообщение не должно требовать `Windows console`; Codex не должен
стартовать.

### MAC-M03. Запуск вне Git working tree

```sh
cd "$STEPAN_TEST_ROOT"
"$STEPAN_BIN"
printf 'exit=%s\n' "$?"
```

Ожидается код `2` и ошибка поиска Git root. Каталог `workspace` внутри текущего
каталога не должен ошибочно считаться root для родительского каталога.

### MAC-M04. Интерактивный старт и ленивый App Server

В терминале A:

```sh
cd "$STEPAN_REPO"
"$STEPAN_BIN"; printf 'stepan-exit=%s\n' "$?"
```

Не вводя `/feature`, в терминале B найдите именно этот экземпляр Stepan:

```sh
STEPAN_PID="$(pgrep -n -x stepan)"
ps -o pid=,ppid=,pgid=,command= -p "$STEPAN_PID"
ps -axo pid=,ppid=,pgid=,command= | awk -v p="$STEPAN_PID" '$2 == p'
```

Ожидается prompt `Command`; непосредственного дочернего `app-server` ещё нет.
Введите `/unknown`: Stepan должен показать короткую ошибку и снова показать
`Command`, не запуская Codex.

### MAC-M05. Полный `/feature`, read-only вопрос и изменение

В том же терминале A введите:

```text
/feature Создай короткую спецификацию команды hello для CLI. Команда печатает hello и завершается кодом 0. Используй feature-id macos-manual-smoke.
```

Если Codex задаёт уточняющий вопрос, ответьте конкретно и продолжайте, пока не
появится экран `Specification: .../specification.md` с меню `/approve`,
`Ask a question`, `Propose a change`.

В терминале B найдите App Server и его process group:

```sh
APP_PID="$(ps -axo pid=,ppid=,pgid=,command= | awk -v p="$STEPAN_PID" '$2 == p && /app-server/ {print $1; exit}')"
APP_PGID="$(ps -o pgid= -p "$APP_PID" | tr -d ' ')"
STEPAN_PGID="$(ps -o pgid= -p "$STEPAN_PID" | tr -d ' ')"
printf 'stepan=%s stepan_pgid=%s app=%s app_pgid=%s\n' "$STEPAN_PID" "$STEPAN_PGID" "$APP_PID" "$APP_PGID"
ps -axo pid=,ppid=,pgid=,command= | awk -v pg="$APP_PGID" '$3 == pg'
```

Ожидается непустой `APP_PID`; `APP_PGID` не совпадает с `STEPAN_PGID`; в группе
есть App Server и, возможно, его потомки.

Путь спецификации берите из заголовка UI. Если Codex выбрал ожидаемый ID:

```sh
export STEPAN_SPEC="$STEPAN_REPO/docs/changes/features/macos-manual-smoke/specification.md"
test -s "$STEPAN_SPEC"
git -C "$STEPAN_REPO" status --short
shasum -a 256 "$STEPAN_SPEC"
```

Если выбран другой допустимый ID, подставьте показанный UI путь в
`STEPAN_SPEC`. В `git status` допустимы изменения только внутри каталога этой
спецификации.

Проверка read-only turn:

1. Сохраните строку `shasum -a 256 "$STEPAN_SPEC"`.
2. В UI выберите `Ask a question`.
3. Введите: `Какой exit code определён для успешного запуска?`
4. После ответа снова вычислите SHA-256.

Ожидается ответ `0`, а hash файла не меняется.

Проверка write-turn:

1. Выберите `Propose a change`.
2. Введите: `Добавь требование завершаться с ненулевым кодом при неизвестном аргументе.`
3. Ответьте на уточнение, если оно появится.
4. Снова вычислите SHA-256 и просмотрите `git status --short`.

Ожидается изменившийся hash; новые изменения остаются только в каталоге
`dirname "$STEPAN_SPEC"`. Затем выберите `/approve`. Stepan должен вернуться к
prompt `Command` без перезапуска приложения.

### MAC-M06. Переиспользование и штатное закрытие

Запомните `APP_PID` из `MAC-M05`. В том же Stepan запустите второй flow:

```text
/feature Создай минимальную спецификацию команды version. Используй feature-id macos-manual-second.
```

Доведите flow до черновика и выберите `/approve`. В терминале B снова получите
непосредственного ребёнка Stepan:

```sh
SECOND_APP_PID="$(ps -axo pid=,ppid=,pgid=,command= | awk -v p="$STEPAN_PID" '$2 == p && /app-server/ {print $1; exit}')"
printf 'first=%s second=%s\n' "$APP_PID" "$SECOND_APP_PID"
```

Ожидается тот же PID: один App Server переиспользован двумя flow. На основном
prompt нажмите `Ctrl+C`. Терминал A должен напечатать `stepan-exit=130`.

Через несколько секунд в терминале B:

```sh
ps -axo pid=,ppid=,pgid=,command= | awk -v pg="$APP_PGID" '$3 == pg'
kill -0 "$APP_PID" 2>/dev/null
printf 'kill-check-exit=%s\n' "$?"
```

Ожидается пустой список process group и `kill-check-exit` не равный нулю.

### MAC-M07. `Ctrl+C` во время активного turn

В терминале A снова запустите Stepan и сразу начните новый flow:

```sh
cd "$STEPAN_REPO"
"$STEPAN_BIN"; printf 'stepan-exit=%s\n' "$?"
```

```text
/feature Проанализируй репозиторий и подготовь подробную спецификацию третьей команды. Используй feature-id macos-interrupt-smoke.
```

Пока turn выполняется, в терминале B заново получите `STEPAN_PID`, `APP_PID` и
`APP_PGID` командами из `MAC-M05`. После проверки строки процесса нажмите
`Ctrl+C` в терминале A.

Ожидается:

- приложение завершается с `stepan-exit=130` без зависания дольше четырёх
  секунд;
- проверка `ps ... | awk -v pg="$APP_PGID" '$3 == pg'` не выводит процессов;
- частично созданная спецификация не выходит за
  `docs/changes/features/macos-interrupt-smoke/`.

### MAC-M08. Сбой App Server и повторный запуск

Снова запустите Stepan и `/feature`, дождитесь появления App Server. В терминале B
получите новый `APP_PID` и сначала убедитесь, что убиваете нужный процесс:

```sh
ps -o pid=,ppid=,pgid=,command= -p "$APP_PID"
```

Только если строка содержит `app-server`, выполните:

```sh
OLD_APP_PID="$APP_PID"
OLD_APP_PGID="$APP_PGID"
kill -KILL "$OLD_APP_PID"
```

Ожидается понятная ошибка flow и возврат к `Command`, а не завершение Stepan.
Старая process group должна исчезнуть. Запустите ещё один `/feature`: должен
появиться новый App Server с PID, отличным от `OLD_APP_PID`. Завершите Stepan по
`Ctrl+C` и ещё раз проверьте отсутствие новой группы.

### MAC-M09. Защита от выхода через symlink

Создайте отдельный Git-репозиторий и подмените его `docs` symlink-ом наружу:

```sh
export STEPAN_ESCAPE_REPO="$STEPAN_TEST_ROOT/escape-workspace"
export STEPAN_OUTSIDE="$STEPAN_TEST_ROOT/outside-write-target"
git init "$STEPAN_ESCAPE_REPO"
git -C "$STEPAN_ESCAPE_REPO" config user.name "Stepan Manual Test"
git -C "$STEPAN_ESCAPE_REPO" config user.email "stepan-manual@example.invalid"
printf '%s\n' '# symlink test' > "$STEPAN_ESCAPE_REPO/README.md"
git -C "$STEPAN_ESCAPE_REPO" add README.md
git -C "$STEPAN_ESCAPE_REPO" commit -m "initial symlink test"
mkdir "$STEPAN_OUTSIDE"
ln -s "$STEPAN_OUTSIDE" "$STEPAN_ESCAPE_REPO/docs"
cd "$STEPAN_ESCAPE_REPO"
"$STEPAN_BIN"; printf 'stepan-exit=%s\n' "$?"
```

Запустите:

```text
/feature Создай минимальную спецификацию. Используй feature-id macos-symlink-escape.
```

Ожидается отказ до write-turn с сообщением, что target выходит из Git root.
Проверьте внешний каталог:

```sh
find "$STEPAN_OUTSIDE" -mindepth 1 -print
```

Команда не должна вывести ни одного пути. Завершите Stepan по `Ctrl+C`.

### MAC-M10. Сохранение exact version pin

Создайте безопасный fake `codex`, который сообщает неправильную версию:

```sh
mkdir "$STEPAN_TEST_ROOT/fake-bin"
printf '%s\n' '#!/bin/sh' 'printf "codex-cli 0.0.0\n"' > "$STEPAN_TEST_ROOT/fake-bin/codex"
chmod +x "$STEPAN_TEST_ROOT/fake-bin/codex"
cd "$STEPAN_REPO"
PATH="$STEPAN_TEST_ROOT/fake-bin:$PATH" "$STEPAN_BIN"; printf 'stepan-exit=%s\n' "$?"
```

В UI запустите любой `/feature`. Ожидается ошибка с фактической версией `0.0.0` и
требованием `0.147.0` до создания thread. Stepan возвращается к `Command`;
завершите его по `Ctrl+C`.

## 4. Короткая Windows-регрессия

`WIN-M01` выполняется на Windows/amd64, потому что реализация меняет общий
process supervisor.

```powershell
$StepanSource = "C:\absolute\path\to\stepan"
$StepanTestRoot = Join-Path ([IO.Path]::GetTempPath()) ("stepan-win-" + [guid]::NewGuid())
$StepanRepo = Join-Path $StepanTestRoot "workspace"
$StepanBin = Join-Path $StepanTestRoot "stepan.exe"
New-Item -ItemType Directory -Force $StepanRepo | Out-Null
Set-Location $StepanSource
go build -o $StepanBin ./cmd/stepan
git init $StepanRepo
git -C $StepanRepo config user.name "Stepan Manual Test"
git -C $StepanRepo config user.email "stepan-manual@example.invalid"
Set-Content -Encoding UTF8 (Join-Path $StepanRepo "README.md") "# Windows regression workspace"
git -C $StepanRepo add README.md
git -C $StepanRepo commit -m "initial test workspace"
Set-Location $StepanRepo
& $StepanBin
```

Выполните один короткий `/feature`, утвердите спецификацию, затем нажмите `Ctrl+C`.
Ожидается прежний Windows UX, код 130 и отсутствие `codex app-server`/его
потомков, запущенных этим экземпляром. Другие работающие сессии Codex завершать
нельзя.

## 5. Итоговый протокол

Заполните таблицу после выполнения:

| ID | Статус | Evidence / замечание |
|---|---|---|
| MAC-M01 |  |  |
| MAC-M02 |  |  |
| MAC-M03 |  |  |
| MAC-M04 |  |  |
| MAC-M05 |  |  |
| MAC-M06 |  |  |
| MAC-M07 |  |  |
| MAC-M08 |  |  |
| MAC-M09 |  |  |
| MAC-M10 |  |  |
| WIN-M01 |  |  |

Также зафиксируйте:

- commit Stepan;
- версия и build macOS;
- модель Mac;
- версии Go, Git и Codex;
- используемый terminal/shell;
- пути или снимки evidence для всех `FAIL`/`BLOCKED`.

Итог `PASS` разрешён только если все строки таблицы имеют статус `PASS`.

## 6. Очистка стенда

Сначала убедитесь, что `STEPAN_TEST_ROOT` указывает на созданный выше временный
каталог. Команда ниже откажется удалять каталог без marker-файла:

```sh
printf 'cleanup target: %s\n' "$STEPAN_TEST_ROOT"
if [ -n "${STEPAN_TEST_ROOT:-}" ] &&
   [ "$STEPAN_TEST_ROOT" != "/" ] &&
   [ -f "$STEPAN_TEST_ROOT/.stepan-manual-test-root" ]; then
  rm -rf -- "$STEPAN_TEST_ROOT"
else
  printf '%s\n' 'cleanup skipped: unexpected target'
fi
```

Удаление необратимо для временных спецификаций и evidence внутри этого
каталога. Сначала скопируйте всё, что нужно приложить к результатам.
