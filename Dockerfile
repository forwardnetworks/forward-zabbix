# fwdzbx: Forward <-> Zabbix integration. Build: docker build -t fwdzbx .
FROM python:3.12-slim AS build
RUN pip install --no-cache-dir uv==0.13.0
WORKDIR /app
COPY pyproject.toml uv.lock ./
COPY src ./src
RUN uv sync --frozen --no-dev

FROM python:3.12-slim
RUN useradd --system --no-create-home --uid 10001 fwdzbx
COPY --from=build /app /app
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1
USER 10001
EXPOSE 8020
ENTRYPOINT ["fwdzbx"]
CMD ["--help"]
