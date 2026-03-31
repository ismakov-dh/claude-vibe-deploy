# vibe-deploy: Python web app (Flask, FastAPI, generic)
FROM python:3.12-slim
WORKDIR /app

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc wget \
    && rm -rf /var/lib/apt/lists/*

# Install Python dependencies
COPY requirements.txt* pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; \
    elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; \
    elif [ -f Pipfile ]; then pip install --no-cache-dir pipenv && pipenv install --deploy --system; \
    fi

# Install gunicorn/uvicorn if not in dependencies
RUN pip install --no-cache-dir gunicorn uvicorn 2>/dev/null || true

COPY . .

ENV PORT=8000
EXPOSE 8000

# Auto-detect framework and start appropriately
CMD ["sh", "-c", "\
    if grep -rq 'FastAPI\\|fastapi' *.py 2>/dev/null; then \
        echo 'Detected FastAPI'; \
        MAIN=$(grep -rl 'app.*=.*FastAPI' *.py 2>/dev/null | head -1 | sed 's/.py$//'); \
        uvicorn ${MAIN:-main}:app --host 0.0.0.0 --port ${PORT}; \
    elif grep -rq 'Flask\\|flask' *.py 2>/dev/null; then \
        echo 'Detected Flask'; \
        MAIN=$(grep -rl 'app.*=.*Flask' *.py 2>/dev/null | head -1 | sed 's/.py$//'); \
        gunicorn --bind 0.0.0.0:${PORT} ${MAIN:-app}:app; \
    elif [ -f app.py ]; then \
        python app.py; \
    elif [ -f main.py ]; then \
        python main.py; \
    else \
        echo 'ERROR: Cannot find entrypoint. Create main.py or app.py.' && exit 1; \
    fi"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT}/ || exit 1
