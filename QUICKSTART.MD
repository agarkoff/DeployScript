# DeployScript

Автоматизация деплоя микросервисов: сборка Maven, git-теги, GitLab CI пайплайны.

## Быстрый старт

```bash
go build -o deploy.exe

# Переменные окружения (один раз)
export GITLAB_TOKEN="glpat-xxxxxxxxxxxx"
export GITLAB_URI="https://gitlab.bft.local"
```

## Команды

### Проезд — полный деплой

```bash
./deploy -c deploy-proezd.yaml -d e:\ecp\proezd -v 34 \
  -m ru/gov/pfr/ecp/apso/proezd -p proezd -n ecp-test,ecp-uat
```

### Проезд — продолжить после сбоя

```bash
./deploy -c deploy-proezd.yaml -v 34 -n ecp-test,ecp-uat --continue
```

### СКЛ — полный деплой

```bash
./deploy -c deploy-skl.yaml -d e:\ecp\skl -v 18 \
  -m ru/gov/pfr/ecp/apso/skl -p skl -n ecp-test
```

### СКЛ — продолжить после сбоя

```bash
./deploy -c deploy-skl.yaml -v 18 -n ecp-test --continue
```

## Параметры

| Флаг | Короткий | Описание |
|------|----------|----------|
| `-config` | `-c` | YAML конфиг (`deploy-proezd.yaml`, `deploy-skl.yaml`) |
| `-version` | `-v` | Номер версии (тег: `X.0.0`) |
| `-namespace` | `-n` | Контуры через запятую (`ecp-test,ecp-uat`) |
| `-directory` | `-d` | Директория с сервисами (не нужен с `--continue`) |
| `-maven-cache-path` | `-m` | Путь Maven кеша (не нужен с `--continue`) |
| `-pom-property-pattern` | `-p` | Паттерн версий в POM (не нужен с `--continue`) |
| `--continue` | | Перезапустить упавшие пайплайны, пропустить сборку |

## Режим `--continue`

Проверяет статусы существующих пайплайнов:
- Успешные — пропускает
- Запущенные — ожидает
- Упавшие — перезапускает

Все контуры обрабатываются параллельно. Ошибки не останавливают остальные сервисы — выводятся сводкой в конце.

## Как работает полный деплой

1. Проверка чистоты git
2. Checkout develop + pull
3. Обновление версий в pom.xml
4. Создание веток `release-{version}` и тегов `{version}.0.0`
5. Maven build (ожидает подтверждения перед push)
6. Push + запуск GitLab CI пайплайнов

Пайплайны запускаются конвейером: сервис стартует на следующем контуре сразу после успеха на предыдущем, не дожидаясь остальных.

## Конфиг (deploy-*.yaml)

```yaml
sequential:
  - name: proezd-api
    directory: proezd-api
    gitlab_project: ecp/proezd/proezd-api
    is_library: true    # только первый контур

groups:
  bo:
    - name: proezd-claim-bo
      directory: proezd-claim-bo
      gitlab_project: ecp/proezd/proezd-claim-bo
```

- `sequential` — деплоятся строго по очереди
- `groups` — внутри группы параллельно, группы между собой по очереди
- `is_library` — деплой только на первый контур
- `is_mesh` — специальная сборка GraphQL Mesh
