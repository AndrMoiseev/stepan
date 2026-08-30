# Stepan Feature Flow

Stepan ведёт локальный управляемый flow от пользовательского намерения до
готовности feature к реализации. Этот glossary фиксирует язык управления
документами, решениями и агентскими сессиями.

## Language

**Feature flow**:
Полный жизненный цикл одной пользовательской инициативы, начатой как feature и
последовательно проходящей управляемые стадии.
_Avoid_: Workflow задачи, сессия агента

**Stage**:
Один обязательный смысловой этап feature flow со своим документом, author-ролью
и пользовательским approval.
_Avoid_: Step, phase

**Intent**:
Уточнённое описание проблемы, ожидаемого результата, scope, exclusions и
существенных ограничений без требований, дизайна и плана реализации.
_Avoid_: Brief, specification

**Specification**:
Утверждаемое описание требований, архитектурных и технических решений,
критериев приёмки и способов проверки feature.
_Avoid_: Intent, implementation plan

**Implementation plan**:
Утверждаемая последовательность зависимых проверяемых задач, трассируемых к
требованиям и решениям specification.
_Avoid_: Specification, task list без трассировки

**Draft**:
Полная предлагаемая редакция документа текущей стадии, ещё не ставшая его
опубликованной ревизией.
_Avoid_: Patch, partial edit

**Published revision**:
Текущее project-owned содержимое документа стадии, с которым сравниваются
последующие drafts и которое может быть утверждено пользователем.
_Avoid_: Approved document

**Revision decision**:
Решение пользователя применить, отклонить или отправить на доработку конкретный
draft относительно опубликованной ревизии.
_Avoid_: Review, approval

**Agent review**:
Необязательная независимая проверка опубликованной specification или
implementation plan отдельной reviewer-ролью.
_Avoid_: Revision decision, stage approval

**Finding**:
Стабильно идентифицируемое замечание agent review с неизменной severity,
решением и текущим статусом.
_Avoid_: Open question

**Stage approval**:
Окончательное решение пользователя, что документ стадии готов и flow может
перейти дальше после успешного phase commit.
_Avoid_: Revision decision, agent review

**Phase commit**:
Git commit всех накопленных файлов текущей feature после stage approval.
_Avoid_: Task commit

**Open question**:
Материальная неоднозначность, которую пользователь явно отложил и которая
блокирует stage approval до явного решения.
_Avoid_: Finding

**System contract**:
Незаменяемые правила роли: протокол, полномочия, машинный формат и допустимые
переходы, имеющие приоритет над содержательными prompts.
_Avoid_: Role prompt, capability

**Role prompt**:
Заменяемая содержательная инструкция о позиции и способе взаимодействия
конкретной agent role.
_Avoid_: System contract, capability

**Capability**:
Заменяемый содержательный prompt-модуль с одной методикой или знанием об
артефакте, подключаемый control plane к определённым ролям.
_Avoid_: System contract, role

