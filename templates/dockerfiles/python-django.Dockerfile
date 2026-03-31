# vibe-deploy: Django application
FROM python:3.12-slim
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc wget libpq-dev \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt* pyproject.toml* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; \
    elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; \
    fi

RUN pip install --no-cache-dir gunicorn 2>/dev/null || true

COPY . .

# Collect static files (skip if it fails)
RUN python manage.py collectstatic --noinput 2>/dev/null || true

# Run migrations on startup, then serve
ENV PORT=8000
EXPOSE 8000

CMD ["sh", "-c", "python manage.py migrate --noinput && gunicorn --bind 0.0.0.0:${PORT} $(find . -name wsgi.py -not -path '*/venv/*' | head -1 | sed 's|./||;s|/|.|g;s|.py$||'):application"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT}/ || exit 1
