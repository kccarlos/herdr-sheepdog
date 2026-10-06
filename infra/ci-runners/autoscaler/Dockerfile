FROM python:3.13-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY main.py .
# One worker, one thread: webhook bursts are handled one at a time, so two
# events never read-modify-write the instance count concurrently.
CMD exec gunicorn --bind :$PORT --workers 1 --threads 1 --timeout 60 main:app
