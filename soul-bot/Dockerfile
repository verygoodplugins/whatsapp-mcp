FROM python:3.12-slim

WORKDIR /app

COPY pyproject.toml ./
COPY soul_bot ./soul_bot
RUN pip install --no-cache-dir .

COPY SOUL.md ./SOUL.md
COPY config.example.yaml ./config.yaml

ENV SOUL_BOT_CONFIG=/app/config.yaml
EXPOSE 8769

CMD ["uvicorn", "soul_bot.app:app", "--host", "0.0.0.0", "--port", "8769"]
