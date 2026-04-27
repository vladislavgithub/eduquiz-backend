# Развёртывание EduQuiz в продакшене

Гайд по запуску полного стека EduQuiz (PostgreSQL + Redis + API + Caddy с auto-TLS + Flutter Web)
на VPS у российского провайдера.

## Что получится в итоге

- HTTPS-домен `eduquiz.example.ru` с автоматическими сертификатами Let's Encrypt.
- REST API на `/api/*`, WebSocket на `/ws/*`.
- Flutter Web SPA как статика на `/`.
- Изолированная docker-сеть; наружу торчат только 80/443/UDP 443 (HTTP/3).

## Выбор VPS и оценка стоимости

| Провайдер              | Тариф (≈ потребление)                  | Цена / мес    |
|------------------------|----------------------------------------|---------------|
| **Selectel Cloud**     | 2 vCPU / 4 GB RAM / 30 GB SSD          | ~500 ₽        |
| **Timeweb Cloud**      | 2 vCPU / 4 GB RAM / 40 GB NVMe         | ~480 ₽        |
| **VK Cloud / RuVDS**   | сопоставимо                            | 450–600 ₽     |

Для MVP с 50–200 одновременных пользователей этого достаточно. ОС — Ubuntu 22.04 / 24.04 LTS.

## 1. Подготовка VPS

### 1.1 Заказ и доступ

1. В панели провайдера создать сервер (Ubuntu 22.04, 2 vCPU / 4 GB).
2. Загрузить публичный SSH-ключ или сохранить root-пароль.
3. Зайти по SSH: `ssh root@<IP>`.

### 1.2 Базовая настройка

```bash
adduser deploy
usermod -aG sudo deploy
mkdir -p /home/deploy/.ssh && cp ~/.ssh/authorized_keys /home/deploy/.ssh/
chown -R deploy:deploy /home/deploy/.ssh
ufw allow OpenSSH && ufw allow 80 && ufw allow 443 && ufw enable
```

### 1.3 Установка Docker

```bash
curl -fsSL https://get.docker.com | sh
usermod -aG docker deploy
systemctl enable --now docker
```

Проверить: `docker compose version` должен показать v2.x.

## 2. DNS

В DNS-зоне домена создать A-запись `eduquiz.example.ru → <IP VPS>`.
Дождаться распространения (`dig +short eduquiz.example.ru` должен отдавать IP сервера).

## 3. Клонирование репозитория

```bash
sudo mkdir -p /opt/eduquiz && sudo chown deploy:deploy /opt/eduquiz
cd /opt && git clone https://github.com/vladislavgithub/eduquiz-backend.git eduquiz
cd /opt/eduquiz
```

## 4. Конфигурация `.env.prod`

```bash
cp .env.prod.example .env.prod
nano .env.prod
```

Обязательно заполнить:

- `DOMAIN` — реальный домен (без протокола).
- `JWT_SECRET` — `openssl rand -base64 32`.
- `POSTGRES_PASSWORD` — `openssl rand -base64 24`.
- `API_IMAGE` — оставить `:latest` или зафиксировать тег (`v0.3.0`).

```bash
chmod 600 .env.prod   # секреты — только владельцу
```

## 5. Flutter Web bundle

Caddy раздаёт SPA из директории `/srv/web` (внутри контейнера), которая монтируется
из `./web` репозитория. Самый простой путь — собрать бандл локально и залить на VPS:

```bash
# на машине разработчика
cd /Users/.../eduquiz-flutter
flutter build web --release --base-href=/

# залить на VPS (флаг --delete очищает старые файлы)
rsync -avz --delete build/web/ deploy@<IP>:/opt/eduquiz/web/
```

Альтернативный, более чистый путь — собрать бандл в CI (отдельный workflow в репо
`eduquiz-flutter`) и публиковать как GitHub Release / в S3-бакет, а на VPS скачивать
артефакт. Пока MVP: достаточно `rsync` после каждого релиза фронта.

> Директория `./web` в репозитории бэкенда зарезервирована под bundle и в коммит не
> попадает (см. `.gitignore`).

## 6. Запуск

```bash
docker compose -f docker-compose.prod.yml --env-file .env.prod pull
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d
```

Что произойдёт:

1. Поднимется Postgres и Redis, дождутся healthcheck.
2. Одноразовый сервис `migrate` применит SQL-миграции из `./migrations`.
3. Стартует `api`.
4. Caddy получит сертификат от Let's Encrypt (HTTP-01 challenge на 80-м порту).

Проверить:

```bash
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs -f caddy
curl -I https://eduquiz.example.ru/healthz
```

## 7. Обновления

### Вручную

```bash
cd /opt/eduquiz
git pull
docker compose -f docker-compose.prod.yml --env-file .env.prod pull
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d
```

### Автоматически через GitHub Actions

В репозитории настроен workflow `.github/workflows/deploy.yml`. При пуше тега `vX.Y.Z`:

1. Собирается amd64-образ и пушится в GHCR (`ghcr.io/vladislavgithub/eduquiz-api`).
2. По SSH на VPS выполняется `git pull && docker compose pull && up -d`.

Перед использованием в `Settings → Secrets and variables → Actions` нужно завести:

- `DEPLOY_HOST` — IP или hostname VPS.
- `DEPLOY_USER` — `deploy`.
- `DEPLOY_KEY` — приватный SSH-ключ (PEM, без пароля), публичная часть — в `~deploy/.ssh/authorized_keys` на VPS.

Тогда релиз делается одной командой:

```bash
git tag v0.3.0 && git push origin v0.3.0
```

## 8. Бэкапы и мониторинг

- **Postgres dump**: `docker compose -f docker-compose.prod.yml exec postgres pg_dump -U eduquiz eduquiz | gzip > backup-$(date +%F).sql.gz`. Заверни в cron.
- **Логи**: `docker compose -f docker-compose.prod.yml logs --tail=200 api`.
- **Uptime**: внешний пинг `https://<DOMAIN>/healthz` (UptimeRobot, Healthchecks.io).

## 9. Откат

```bash
# зафиксированный образ + откат миграций при необходимости
sed -i 's|:latest|:v0.2.9|' .env.prod   # или поправить вручную
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d
```

Миграции имеют `down`-скрипты в `./migrations/*.down.sql`, прогон вручную:

```bash
docker run --rm --network eduquiz_eduquiz_internal \
  -v $(pwd)/migrations:/migrations migrate/migrate:v4.17.1 \
  -path=/migrations -database "$DATABASE_URL" down 1
```

## Устранение типовых проблем

| Симптом                                         | Что проверить                                                  |
|-------------------------------------------------|----------------------------------------------------------------|
| Caddy не получает сертификат                    | DNS на правильный IP; 80/tcp открыт; нет другого процесса на 80 |
| `migrate` падает с `connection refused`         | postgres не успел подняться; перезапустить `docker compose up -d` |
| API: `JWT_SECRET must be set in production`     | Заполни `.env.prod`                                            |
| Flutter Web показывает 404 при F5               | Убедись, что Caddyfile содержит `try_files {path} /index.html` |
