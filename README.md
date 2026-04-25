# EduQuiz Backend

Backend сервиса интерактивного тестирования EduQuiz.

**Стек:** Go 1.22+ · Gin · PostgreSQL 16 · Redis 7 · WebSocket · JWT · Docker

**Часть магистерской работы Дворянкина В.В. (АСМ-24-04, РГУ Нефти и Газа им. Губкина)**
Тема: «Разработка моделей и сервиса для интерактивного взаимодействия с обучающимися»
Научный руководитель: Волков Д.А., к.т.н., доцент кафедры АСУ.

## Связанные репозитории

- [eduquiz-flutter](../../eduquiz-flutter) — кроссплатформенный клиент (Flutter)
- [vkr-dvoryankin](../../vkr-dvoryankin) — текст ВКР в LaTeX

## Запуск (development)

```bash
docker compose up -d   # postgres + redis
go run ./cmd/api
```
