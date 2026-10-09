# Происхождение и сопровождение

Локальная адаптация материалов [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) для разработки и ревью `stepan`.

Исходная версия: commit `19a0626ae8565d27a7b7bdf59d8d99d94d7e284c`, просмотрен 2026-09-21. Материалы доступны в [закреплённом дереве skills](https://github.com/samber/cc-skills-golang/tree/19a0626ae8565d27a7b7bdf59d8d99d94d7e284c/skills). Уведомление upstream сохранено в [LICENSE.upstream](LICENSE.upstream).

| Локальная тема | Исходные каталоги в `skills/` |
| --- | --- |
| `style.md` | `golang-code-style`, `golang-naming`, `golang-documentation` |
| `errors.md` | `golang-error-handling` |
| `design.md` | `golang-structs-interfaces`, `golang-dependency-injection`, `golang-project-layout` |
| `safety.md` | `golang-safety`, `golang-data-structures` |
| `concurrency.md` | `golang-concurrency`, `golang-context` |
| `testing.md` | `golang-testing` |

Формулировки сокращены и адаптированы: требования проекта имеют приоритет; для общих советов указаны условия и исключения. Не перенесены personas, инструкции оркестрации агентов, правила конкретных фреймворков, обязательные сторонние генераторы, универсальный тег `integration`, лимиты длины функций и безусловные запреты nil-срезов, указателей в каналах или возврата ошибки без обёртки. Инструменты в `tools.md` подключены отдельным решением проекта.

При обновлении сравнивайте выбранные темы с закреплённым commit, проверяйте API по версии Go проекта и обновляйте этот документ. Новую норму размещайте в одной теме; ссылку в соседней теме добавляйте с условием чтения. Проверяйте индекс на задачах: переименование, обработка ошибки, изменение интерфейса, передача среза, shutdown процесса, исправление нестабильного теста. Каждая задача должна приводить к нужным темам без загрузки всего каталога.
