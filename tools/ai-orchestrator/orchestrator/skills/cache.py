from __future__ import annotations

from hashlib import sha256
import io
from pathlib import Path
import zipfile

import requests


def normalize_digest(value: str) -> str:
    normalized = str(value or "").strip().lower()
    if normalized.startswith("sha256:"):
        normalized = normalized[7:]
    if len(normalized) != 64:
        raise ValueError("skill bundle digest must be 64-char sha256 hex")
    return normalized


def digest_bytes(raw: bytes) -> str:
    return sha256(raw).hexdigest()


def download_bundle(artifact_url: str, *, timeout_s: float) -> bytes:
    normalized = str(artifact_url or "").strip()
    if not normalized:
        raise ValueError("artifact_url is required")
    if normalized.startswith("file://"):
        return Path(normalized[7:]).read_bytes()
    response = requests.get(normalized, timeout=max(float(timeout_s), 1.0))
    response.raise_for_status()
    return response.content


def prepare_bundle(cache_dir: str, *, artifact_url: str, bundle_digest: str, timeout_s: float) -> Path:
    digest = normalize_digest(bundle_digest)
    target_dir = Path(cache_dir).expanduser() / digest
    manifest_path = target_dir / "manifest.json"
    if manifest_path.exists():
        return target_dir

    raw = download_bundle(artifact_url, timeout_s=timeout_s)
    if digest_bytes(raw) != digest:
        raise RuntimeError("skill bundle digest mismatch")

    target_dir.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        _extract_bundle_safely(archive, target_dir)
    if not manifest_path.exists():
        raise RuntimeError("skill bundle missing manifest.json")
    return target_dir


def _extract_bundle_safely(archive: zipfile.ZipFile, target_dir: Path) -> None:
    target_root = target_dir.resolve()
    for member in archive.infolist():
        member_path = target_dir / member.filename
        resolved = member_path.resolve()
        try:
            resolved.relative_to(target_root)
        except ValueError as exc:
            raise RuntimeError(f"skill bundle contains unsafe path: {member.filename}") from exc
        archive.extract(member, target_dir)
