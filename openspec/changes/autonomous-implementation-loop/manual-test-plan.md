# Ручная приёмка автономного implementation loop

Статус этого плана: `not_run`. Он описывает live smoke и сквозную приёмку,
которую выполняет пользователь. Автоматические conformance-тесты с
детерминированными transport/runtime — отдельное доказательство границ
адаптеров; они не подтверждают работу настоящих Codex, Claude или Nessy.

Для каждого результата записывайте `PASS`, `FAIL`, `BLOCKED` либо `not_run`,
дату, ОС/архитектуру, commit Stepan, выбранный provider/model/reasoning и
безопасные выдержки из stdout/stderr. Не включайте токены, cookies, полный
environment, settings.json или provider session IDs в evidence.

## Стенд

Используйте новый одноразовый локальный Git-репозиторий, не worktree и не
основную ветку. Нужны собранный Stepan, Git, Go 1.26.5, один выбранный
аутентифицированный provider CLI и доступный OpenSpec CLI. Не задавайте
provider credentials через этот план: настройте их заранее в доверенном
пользовательском хранилище выбранного CLI. Для Nessy настройте
`nessy.auth_token` в `~/.stepan/settings.json`, не копируя значение в отчёт.

Создайте репозиторий с небольшой безопасной задачей: добавить строку
`autonomous smoke` в `README.md`. В нём должны быть готовые OpenSpec
`proposal.md`, `design.md`, четыре spec-файла и `tasks.md` с одной конечной
задачей. Создайте локальную ветку `smoke/autonomous-loop` и начальный commit.
Команды проверки не должны обращаться к сети или менять файлы вне этого
репозитория.

Добавьте `.stepan/settings.json` с проектной частью `implementation`: один
профиль выбранного provider с его поддерживаемой моделью, все шесть loop-ролей
назначены этому профилю, `main_branch` указывает на исходную ветку, и есть
одна обязательная быстрая проверка. Например, замените два заполнителя перед
запуском:

```json
{
  "implementation": {
    "profiles": {
      "smoke": {
        "provider": "REPLACE_PROVIDER",
        "model": "REPLACE_SUPPORTED_MODEL"
      }
    },
    "roles": {
      "orchestrator": "smoke",
      "briefer": "smoke",
      "implementer": "smoke",
      "task_reviewer": "smoke",
      "explorer": "smoke",
      "final_reviewer": "smoke"
    },
    "main_branch": "main",
    "checks": {
      "unit": {
        "kind": "tests",
        "command": { "program": "go", "args": ["test", "./..."] },
        "cwd": ".",
        "timeout_seconds": 600
      }
    },
    "required_checks": ["unit"]
  }
}
```

If the provider requires a nonempty reasoning value, add its supported value
to the profile. Confirm that the configuration validates before any agent is
started. Commit the fixture, then create the smoke branch from it so the new
run starts with a clean working tree.

Run the built binary from the disposable repository with exactly one provider:

```text
stepan --agent codex
stepan --agent claude
stepan --agent nessy
```

Run only the command for the adapter under test. Do not run provider commands
from an automated test or CI job. Observe `git status --short`, `git log`, and
the disposable run directory before and after every scenario.

## Common scenarios

For each provider complete these five scenarios in the same disposable
repository, recreating it if an interruption leaves intentional uncommitted
work. The expected structured fields are controller-owned JSON responses; do
not treat conversational prose as a successful response.

### M01 — clean start and structured response

Start `/implement <change-name>`. Confirm that Stepan validates the clean
branch and required checks, starts each required role through the selected
adapter, and accepts only a valid structured response. Capture the redacted
terminal output and the run's persisted operation/result records. Deliberately
return malformed JSON once only if the provider UI makes this safely possible:
the controller must report/retry according to its technical-attempt policy and
must not advance on the malformed result.

Expected: no push, pull request, merge, deployment, shell command requested by
the agent, or provider-specific thread history becomes a control-plane fact.

### M02 — allowed workspace write

Let the implementer make the planned `README.md` change. Confirm that the
write appears only in the disposable workspace and becomes part of the task
diff. The reviewer and Explorer must remain read-only. Verify that a file
outside the repository and an unconfigured artifact path were not created or
modified.

Expected: the controller, not agent text, runs the configured check and makes
the local commit only after current required-check and review evidence.

### M03 — Explorer round trip

Ask the active implementer or briefer to use Explorer for a precise fact about
the one-file fixture. Confirm a fresh Explorer session is created, its result
is returned to the source role, and the original source session continues.
Ask Explorer to edit a file or run a command.

Expected: Explorer returns facts/unknowns/references only; it neither edits nor
executes. The run records the Explorer episode and no independent task or
provider transcript is substituted for its structured result.

### M04 — user interruption and recovery

During a deliberately slow but safe agent turn, issue `/pause` (or Ctrl+C if
the interactive UI maps it to the documented interrupt). Record process IDs
and `git status --short` before interruption and after the process has exited.
Restart Stepan and inspect status without `/resume`; then issue `/resume`.

Expected: the active adapter turn is interrupted, the run is durably paused,
already allowed changes are preserved, no new agent/check begins before the
explicit resume, and the resume begins the full required check set. A closed
run is never reported as success solely because it was interrupted.

### M05 — completion and isolation

Finish the one task and final review. Confirm the resulting local commit has
the Stepan trailer fields, the task is completed only after that commit, and
the branch stays local. Check that no provider process remains after normal
exit. Delete the disposable repository only after collecting evidence; never
reuse it as a production repository.

## Per-adapter result record

| Adapter | M01 structured | M02 workspace write | M03 Explorer | M04 interrupt/resume | M05 local completion | Overall |
| --- | --- | --- | --- | --- | --- | --- |
| Codex | `not_run` | `not_run` | `not_run` | `not_run` | `not_run` | `not_run` |
| Claude | `not_run` | `not_run` | `not_run` | `not_run` | `not_run` | `not_run` |
| Nessy | `not_run` | `not_run` | `not_run` | `not_run` | `not_run` | `not_run` |

`PASS` requires all five scenarios for that adapter and safe evidence. A
missing provider, credentials, or supported platform is `BLOCKED`, not `PASS`.
Keep fake-runtime/unit-test results in a separate automated-test record.
