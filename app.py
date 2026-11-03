"""Same-origin deployment: the API and the installable mobile/desktop UI."""
from pathlib import Path
from fastapi.staticfiles import StaticFiles
from backend.main import app

assets = Path(__file__).parent / "frontend" / "dist"
if not (assets / "index.html").is_file():
    raise RuntimeError("Build the frontend first: cd frontend && npm ci && npm run build")
app.mount("/", StaticFiles(directory=assets, html=True), name="web")
