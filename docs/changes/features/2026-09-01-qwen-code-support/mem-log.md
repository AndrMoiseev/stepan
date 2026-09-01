# Feature memory log

Feature: 2026-09-01-qwen-code-support

## Entry 000001

Stage: intent
Role: intent-author
Event: brief
At: 2026-09-01T21:14:49.5084662+03:00
Previous: root
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 54
Checksum: b7c19ee3011e76478ade7b1d066f76758095487d4dd876ad3efaa4ba8fb78ef7

Хочу добавить поддержку Qwen Code

## Entry 000002

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:15:27.4731277+03:00
Previous: b7c19ee3011e76478ade7b1d066f76758095487d4dd876ad3efaa4ba8fb78ef7
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 320
Checksum: acea16ae537ad8687121eb382d4a7e3079f082ce1ef3beb0cc30b221021bde19

## Decision D-001

author: user

decision: Добавить поддержку Qwen Code в Stepan.

rationale: Это исходная цель функции, сформулированная пользователем.

alternatives: Сохранить поддержку только Codex и Claude.

supersedes: []

## Entry 000003

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:15:27.5081848+03:00
Previous: acea16ae537ad8687121eb382d4a7e3079f082ce1ef3beb0cc30b221021bde19
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 969
Checksum: ab24bb1daffab825e1453608fbaeb038610026a8b8405f9dfa40542c33f8a38f

Сейчас Stepan предлагает пользователю только Codex и Claude; при этом режим Claude уже допускает совместимый сторонний CLI, переданный явным абсолютным путём (README.md, cmd/stepan/agent_config.go, ADR 0004). Какой наблюдаемый результат нужен для Qwen Code: отдельный явно выбираемый пользователем Qwen Code в Stepan или подтверждённая и документированная совместимость Qwen Code с существующим режимом Claude? Отдельный выбор делает Qwen самостоятельным поддерживаемым продуктом; использование режима Claude сохраняет текущую поверхность, но Qwen не будет виден как отдельный вариант.

## Entry 000004

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:29:11.9790218+03:00
Previous: ab24bb1daffab825e1453608fbaeb038610026a8b8405f9dfa40542c33f8a38f
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 121
Checksum: 87de897d219628c56c4dd493fc6cd7bf028e1ced52a4c909649c3231f7639761

а qwen разве совместим с Claude Code? Можешь предоставить доказательства?

## Entry 000005

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:29:45.9350444+03:00
Previous: 87de897d219628c56c4dd493fc6cd7bf028e1ced52a4c909649c3231f7639761
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 2846
Checksum: 56abdbe6c900707bdf538ac126f2f79b048825238c3f5ea9dda48d11bf93577e

Нет — подтверждений совместимости Qwen Code с Claude Code CLI/SDK я не нашёл. Моя предыдущая формулировка была слишком широкой.

Доказательства из официальных источников:

- Qwen Code использует собственные поверхности интеграции: `qwen` CLI, `@qwen-code/sdk`, ACP и `qwen serve`. Это отдельные протоколы и способы управления процессом ([архитектура Qwen Code](https://qwenlm.github.io/qwen-code-docs/en/developers/architecture/)).
- Его TypeScript SDK требует установленный Qwen Code и принимает путь именно к исполняемому файлу Qwen через `pathToQwenExecutable` ([документация SDK](https://github.com/QwenLM/qwen-code/blob/main/docs/developers/sdk-typescript.md)). Это не Claude Agent SDK.
- Поддержка `anthropic` в Qwen Code означает использование Anthropic API как поставщика моделей через `@anthropic-ai/sdk`, а не совместимость Qwen CLI с управляющим протоколом Claude Code ([документация провайдеров](https://github.com/QwenLM/qwen-code/blob/main/docs/users/configuration/model-providers.md)).
- Сам проект говорит о функциональном паритете с Claude Code, но не о взаимозаменяемости CLI или SDK ([официальный репозиторий Qwen Code](https://github.com/QwenLM/qwen-code)). Функциональное сходство — инструменты, сессии, headless-режим — не гарантирует протокольную совместимость.

Это существенно для Stepan: существующий режим Claude запускается через `claude-agent-sdk-go` и ожидает Claude-совместимый SDK-протокол. Официальные материалы Qwen показывают другую интеграционную поверхность, поэтому запуск `qwen` через текущий режим Claude нельзя считать поддерживаемым без отдельного доказательного теста; оснований ожидать успеха сейчас нет.

Предлагаю зафиксировать intent как отдельную, явно выбираемую поддержку Qwen Code с использованием его собственной поддерживаемой интеграционной поверхности. Это и есть желаемое направление?

## Entry 000006

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:30:42.8023866+03:00
Previous: 56abdbe6c900707bdf538ac126f2f79b048825238c3f5ea9dda48d11bf93577e
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 4
Checksum: 173d959cec322e549109a541dcf73b2ff99d7b544b819d97bea754f0ead8aca1

да

## Entry 000007

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:30:53.2065876+03:00
Previous: 173d959cec322e549109a541dcf73b2ff99d7b544b819d97bea754f0ead8aca1
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 873
Checksum: f4269f83da8fb463d6508002ed2697886608c04e2831f6a992901087c801d09b

## Decision D-002

author: user

decision: Qwen Code должен поддерживаться как отдельный, явно выбираемый агентский runtime через собственную поддерживаемую интеграционную поверхность Qwen Code.

rationale: Официальные материалы не подтверждают совместимость Qwen Code с управляющим протоколом Claude Code; пользователь подтвердил отдельное направление интеграции.

alternatives: Использовать существующий режим Claude как неподтверждённо совместимый путь.; Не выделять Qwen Code как отдельный пользовательский вариант.

supersedes: []

## Entry 000008

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:30:53.248841+03:00
Previous: f4269f83da8fb463d6508002ed2697886608c04e2831f6a992901087c801d09b
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 1015
Checksum: c8820a21cc1719dd1b8182ede1fcdce78c76da4dd697f01437b2f3e8643fdc7a

Зафиксировал Qwen Code как отдельный, явно выбираемый агентский runtime, использующий собственную поддерживаемую интеграционную поверхность Qwen, а не режим совместимости Claude.

Какой объём пользовательского результата нужен: полный `/feature` flow с Qwen Code на обеих поддерживаемых Stepan платформах — Windows/amd64 и Apple Silicon macOS — с теми же этапами, решениями пользователя и явными ошибками, что у существующих агентов; или на первом шаге достаточно одной платформы либо части flow? Полный паритет задаёт ясный результат, но заметно расширяет объём совместимости и ручной проверки.

## Entry 000009

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:32:00.4746538+03:00
Previous: c8820a21cc1719dd1b8182ede1fcdce78c76da4dd697f01437b2f3e8643fdc7a
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 44
Checksum: 2297d443c9db77146d31fe6917df694c274329fd962d403af04126e704e7648e

да, нужен полный паритет

## Entry 000010

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:32:10.8157996+03:00
Previous: 2297d443c9db77146d31fe6917df694c274329fd962d403af04126e704e7648e
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 626
Checksum: 61d9c93bfc4290b69bcd4b769d214bfd18d067e6477dfd07c1bfa1593db1deb1

## Decision D-003

author: user

decision: Поддержка Qwen Code должна обеспечивать полный пользовательский паритет всего `/feature` flow на Windows/amd64 и Apple Silicon macOS.

rationale: Пользователь явно выбрал полный паритет вместо ограниченного этапами или одной платформой первого выпуска.

alternatives: Поддержать только часть `/feature` flow.; Ограничить первый выпуск одной платформой.

supersedes: []

## Entry 000011

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:32:10.8549932+03:00
Previous: 61d9c93bfc4290b69bcd4b769d214bfd18d067e6477dfd07c1bfa1593db1deb1
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 1150
Checksum: 8f2aca9d8996f5373ebc7959893813c2312f6b67f16f1672298b1b928a76afc1

Полный паритет означает, что пользователь может выбрать Qwen Code и пройти весь `/feature` flow на Windows/amd64 и Apple Silicon macOS с теми же этапами, решениями и наблюдаемыми ошибками, что и с существующими агентами.

Нужно отдельно зафиксировать границу безопасности. Должен ли Qwen Code сохранять действующую модель Stepan: проект доступен только для чтения, запись разрешена лишь в назначенный корень текущего артефакта, а настройки и внешние возможности Qwen не могут незаметно расширить эти права? Это строгий паритет безопасности. Альтернатива — паритет только пользовательского flow с принятием специфичных для Qwen дополнительных возможностей и рисков.

## Entry 000012

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:33:18.5644354+03:00
Previous: 8f2aca9d8996f5373ebc7959893813c2312f6b67f16f1672298b1b928a76afc1
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 96
Checksum: b1f89c1cce46c92770083740c4b1b6224278e433e4af80ef1290fc876068e84d

да, полностью аналогично уже поддерживаемым агентам

## Entry 000013

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:33:31.1340848+03:00
Previous: b1f89c1cce46c92770083740c4b1b6224278e433e4af80ef1290fc876068e84d
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 828
Checksum: 5c8e9ab5c0dc632b17be9c2fb9d66cb519ca46ccd35a29c58d8dc35b00447f68

## Decision D-004

author: user

decision: Qwen Code должен соблюдать те же границы безопасности и контроля Stepan, что и уже поддерживаемые агенты.

rationale: Пользователь потребовал полностью аналогичное поведение, включая read-only доступ к проекту, запись только в назначенный корень артефакта и отсутствие скрытого расширения возможностей через настройки Qwen.

alternatives: Обеспечить только функциональный паритет flow и принять специфичные для Qwen дополнительные права и риски.

supersedes: []

## Entry 000014

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:33:31.1744129+03:00
Previous: 5c8e9ab5c0dc632b17be9c2fb9d66cb519ca46ccd35a29c58d8dc35b00447f68
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 846
Checksum: b8d683d87e57b96873dc5713207349d9080a770b1405a10230239ac1f3f15f02

Зафиксировал: для Qwen Code нужен тот же уровень изоляции и контроля Stepan, что для уже поддерживаемых агентов; переход на Qwen не должен ослаблять границы чтения, записи или внешних возможностей.

Должны ли Codex остаться агентом по умолчанию, а Qwen Code — явно выбираемым вариантом, который пользователь устанавливает и настраивает заранее? Альтернатива — менять значение по умолчанию или включать установку и настройку Qwen в эту функцию, что заметно расширит её назначение.

## Entry 000015

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:34:07.823321+03:00
Previous: b8d683d87e57b96873dc5713207349d9080a770b1405a10230239ac1f3f15f02
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 50
Checksum: 5904c1c74a9567f7062ce56b9634badc471b6df09c666d29df244ba1502d4cd1

да, выбираемый явно вариант

## Entry 000016

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:34:16.1039848+03:00
Previous: 5904c1c74a9567f7062ce56b9634badc471b6df09c666d29df244ba1502d4cd1
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 472
Checksum: a98110b6af847dd996741f2359c508d286a1045fe7833b700e5fb8da2388aa24

## Decision D-005

author: user

decision: Qwen Code должен быть явно выбираемым вариантом и не заменять Codex по умолчанию.

rationale: Пользователь подтвердил opt-in модель подключения Qwen Code.

alternatives: Сделать Qwen Code агентом по умолчанию.; Автоматически выбирать Qwen Code при его наличии.

supersedes: []

## Entry 000017

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:34:16.1466425+03:00
Previous: a98110b6af847dd996741f2359c508d286a1045fe7833b700e5fb8da2388aa24
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 878
Checksum: a572d5cd3792482733f6bf686bf1158b40be629052effbb93a7b30ee5b765ccf

Qwen Code будет явно выбираемым вариантом; Codex останется поведением по умолчанию. Установка, авторизация и первоначальная настройка Qwen Code останутся ответственностью пользователя.

Нужно определить границу совместимости: поддерживаем только официальный Qwen Code или также сторонние CLI, заявляющие совместимость с его SDK/протоколом? Поддержка только официального продукта даёт проверяемую границу; поддержка совместимых форков потребует отдельного обещания совместимости и проверок.

## Entry 000018

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:35:02.2594007+03:00
Previous: a572d5cd3792482733f6bf686bf1158b40be629052effbb93a7b30ee5b765ccf
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 116
Checksum: 30a1bf2bbe5a04e0ae0be4279c2d45daf8c819435f9baad3302a7bb5dc102c15

поддерживаем в том числе сторонние CLI, совместимые по протоколу

## Entry 000019

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:35:12.5292079+03:00
Previous: 30a1bf2bbe5a04e0ae0be4279c2d45daf8c819435f9baad3302a7bb5dc102c15
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 658
Checksum: e834078b3561100cb3eb0396c1fadbfabe0518e4f3f44c33633efef760431b7f

## Decision D-006

author: user

decision: Поддержка должна распространяться на официальный Qwen Code и сторонние CLI, совместимые с ним по протоколу.

rationale: Пользователь хочет определять совместимость по фактическому протоколу, а не по поставщику, имени executable или бренду.

alternatives: Поддерживать только официальный Qwen Code.; Поддерживать заранее перечисленные сторонние сборки.

supersedes: []

## Entry 000020

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:35:12.5399447+03:00
Previous: e834078b3561100cb3eb0396c1fadbfabe0518e4f3f44c33633efef760431b7f
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 958
Checksum: aea45fbf1842abe115197a072c156be53f8a15376440e256ea73ed925346198f

Объём включает официальный Qwen Code и сторонние CLI независимо от имени и бренда, если они действительно совместимы по протоколу.

Какое обещание совместимости нужно дать: поддерживать зафиксированную и проверенную версию протокола Qwen Code, отклоняя несовместимые варианты с понятной диагностикой, или обещать работу с любой текущей и будущей версией, заявляющей совместимость? Зафиксированный протокол даёт воспроизводимую поддержку; открытое обещание повышает удобство, но делает поведение зависимым от внешних изменений.

## Entry 000021

Stage: intent
Role: intent-author
Event: user_message
At: 2026-09-01T21:36:00.0604778+03:00
Previous: aea45fbf1842abe115197a072c156be53f8a15376440e256ea73ed925346198f
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 36
Checksum: c01797ddf0a309cea77fb83bf0d491acfb3c09f44393429cf290cc37b5159966

не проверяем версию

## Entry 000022

Stage: intent
Role: intent-author
Event: decision
At: 2026-09-01T21:36:39.4652106+03:00
Previous: c01797ddf0a309cea77fb83bf0d491acfb3c09f44393429cf290cc37b5159966
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 602
Checksum: b4c72b54473dc1125c322248b1e6a72afdb4d2bd62fa76094c8bddc33c122d5d

## Decision D-007

author: user

decision: Stepan не должен проверять или ограничивать версию Qwen Code либо совместимого стороннего CLI.

rationale: Пользователь явно отказался от version gate; пригодность определяется совместимостью по протоколу.

alternatives: Поддерживать только зафиксированную версию протокола.; Поддерживать ограниченный диапазон версий.

supersedes: []

## Entry 000023

Stage: intent
Role: intent-author
Event: attempt
At: 2026-09-01T21:36:39.4770231+03:00
Previous: b4c72b54473dc1125c322248b1e6a72afdb4d2bd62fa76094c8bddc33c122d5d
State-Hash: 690c5549419ed6d311b5f8245d61c02c6e0ab1bfd00068578788fc2971afc767
Body-Length: 128
Checksum: 67fa8850cf80ae005e027470fde4fb540a07e2d73d9262cbd3d77ab025e046e0

author draft observed for intent: hash=c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c valid=true diagnostics=0

## Entry 000024

Stage: intent
Role: intent-author
Event: agent_message
At: 2026-09-01T21:36:39.4977962+03:00
Previous: 67fa8850cf80ae005e027470fde4fb540a07e2d73d9262cbd3d77ab025e046e0
State-Hash: aad95dc847d7e19b163f2b9d1493cd1cd99eff974124ebea1ba18a4872b81c09
Body-Length: 104
Checksum: b6e3df252fc81107e82512cd735e9c5fb19b5a95645520241b10143cdbcdf50a

author draft published for intent: hash=c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c

## Entry 000025

Stage: intent
Role: intent-author
Event: approval
At: 2026-09-01T21:38:09.0714654+03:00
Previous: b6e3df252fc81107e82512cd735e9c5fb19b5a95645520241b10143cdbcdf50a
State-Hash: 8dfaf2964c432ae2748f015ef4830459866f995766c3ddf05c482323ecd2ba78
Body-Length: 86
Checksum: 77a85d76e3e3a352caf32f80540e6e53974456f1b1d84e1ed9f1792f5c982c5e

intent approved: hash=c1d7f8ea58d29087964f985c726ec459860bc8566912cd901b3ec06bf25cd52c

## Entry 000026

Stage: intent
Role: intent-author
Event: commit
At: 2026-09-01T21:38:09.0714654+03:00
Previous: 77a85d76e3e3a352caf32f80540e6e53974456f1b1d84e1ed9f1792f5c982c5e
State-Hash: 8dfaf2964c432ae2748f015ef4830459866f995766c3ddf05c482323ecd2ba78
Body-Length: 67
Checksum: e129ba804241ab8809fea04e8232249b9536b5496e1d7c69b057fda23a52fe7f

phase commit: feature(2026-09-01-qwen-code-support): approve intent
