#!/usr/bin/env python3
import hashlib
import html
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from typing import Literal, overload
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parent.parent
BASE_URL = os.environ["PAGE_URL"].rstrip("/") + "/openwrt"
SDK_RELEASE = os.environ["OPENWRT_RELEASE"]
if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", SDK_RELEASE):
    raise ValueError("OPENWRT_RELEASE must be a full SDK release, for example 25.12.6")
RELEASE = SDK_RELEASE.rsplit(".", 1)[0]
PACKAGES = {
    "updns": "updns",
    "luci-app-updns": "luci",
    "luci-i18n-updns-zh-cn": "luci",
}


def versions():
    result = {}
    for group, directory in (("updns", "updns"), ("luci", "luci-app-updns")):
        content = (ROOT / "openwrt" / directory / "Makefile").read_text()
        values = {}
        for field in ("PKG_VERSION", "PKG_RELEASE"):
            matches = re.findall(r"^" + field + r":=([A-Za-z0-9._+~-]+)$", content, re.M)
            if len(matches) != 1:
                raise ValueError(f"Expected one literal {field} in {directory}/Makefile")
            values[field] = matches[0]
        result[group] = values["PKG_VERSION"] + "-r" + values["PKG_RELEASE"]
    return result


@overload
def fetch(path: str, allow_missing: Literal[False] = False) -> bytes: ...


@overload
def fetch(path: str, allow_missing: Literal[True]) -> bytes | None: ...


def fetch(path: str, allow_missing: bool = False) -> bytes | None:
    try:
        with urllib.request.urlopen(BASE_URL + "/" + path, timeout=60) as response:
            return response.read()
    except urllib.error.HTTPError as error:
        if allow_missing and error.code == 404:
            error.close()
            return None
        raise


def verify_signature(data, signature, public_key):
    with tempfile.TemporaryDirectory() as directory:
        key_path = Path(directory) / "public.pem"
        signature_path = Path(directory) / "signature"
        key_path.write_bytes(public_key)
        signature_path.write_bytes(signature)
        subprocess.run(
            ["openssl", "dgst", "-sha256", "-verify", str(key_path),
             "-signature", str(signature_path)],
            input=data,
            stdout=subprocess.DEVNULL,
            check=True,
        )


def sign(data, key):
    return subprocess.check_output(["openssl", "dgst", "-sha256", "-sign", key], input=data)


def previous(public_key=None):
    data = fetch(RELEASE + "/manifest.json", allow_missing=True)
    if data is None:
        return None
    if public_key is not None:
        verify_signature(data, fetch(RELEASE + "/manifest.json.sig"), public_key)
    result = json.loads(data)
    if result["release"] != RELEASE:
        raise ValueError("Published manifest has an unexpected OpenWrt release")
    return result


def prepare():
    old = previous()
    current = versions()
    changed = (
        old is None
        or old["versions"] != current
        or old.get("sdk_release") != SDK_RELEASE
    )
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"publish={'true' if changed else 'false'}\n")
    print("Package versions:", current)
    print("SDK release:", SDK_RELEASE)
    print("Published versions:", old["versions"] if old else "none")


def publish():
    old = previous()
    current = versions()
    apk = os.environ["APK_TOOL"]
    key = os.environ["APK_KEY"]
    site = Path(os.environ["OUTPUT_DIR"])
    output = site / "openwrt"
    inputs = Path(os.environ["INPUT_DIR"])
    architectures = json.loads(os.environ["ARCHITECTURES"])
    public_key = subprocess.check_output(["openssl", "pkey", "-in", key, "-pubout"])
    key_digest = hashlib.sha256(public_key).hexdigest()
    registry_data = fetch("releases.json", allow_missing=True)
    if registry_data is None:
        if old is not None or fetch("signing.pem", allow_missing=True) is not None:
            raise ValueError("Existing repository has no release registry")
        releases = []
    else:
        releases = json.loads(registry_data)["releases"]
        if not isinstance(releases, list) or len(set(releases)) != len(releases):
            raise ValueError("Invalid release registry")
        if fetch("signing.pem") != public_key:
            raise ValueError("Signing key changed; refusing to replace the trusted repository")
        verify_signature(registry_data, fetch("releases.json.sig"), public_key)
        if (RELEASE in releases) != (old is not None):
            raise ValueError("Release registry and published manifest do not match")
        if old is not None:
            old = previous(public_key)
            if old is None:
                raise ValueError("Published manifest disappeared")

    if old:
        if old["public_key_sha256"] != key_digest:
            raise ValueError("Signing key changed; refusing to replace the trusted repository")
        for group, new in current.items():
            comparison = subprocess.check_output(
                [apk, "version", "-t", old["versions"][group], new], text=True
            ).strip()
            if comparison != ("=" if old["versions"][group] == new else "<"):
                raise ValueError(f"Package version cannot go backwards: {group}")

    output.mkdir(parents=True, exist_ok=True)
    (output / "signing.pem").write_bytes(public_key)
    for release in releases:
        if not isinstance(release, str) or not re.fullmatch(r"[0-9]+\.[0-9]+", release):
            raise ValueError("Invalid release in registry")
        if release == RELEASE:
            continue
        data = fetch(f"{release}/manifest.json")
        signature = fetch(f"{release}/manifest.json.sig")
        verify_signature(data, signature, public_key)
        retained = json.loads(data)
        if retained["release"] != release or retained["public_key_sha256"] != key_digest:
            raise ValueError(f"Invalid manifest for {release}")
        release_dir = output / release
        release_dir.mkdir()
        (release_dir / "manifest.json").write_bytes(data)
        (release_dir / "manifest.json.sig").write_bytes(signature)
        for arch, entries in retained["packages"].items():
            if not re.fullmatch(r"[a-z0-9_-]+", arch):
                raise ValueError(f"Invalid architecture: {arch}")
            directory = release_dir / arch
            directory.mkdir()
            (directory / "packages.adb").write_bytes(fetch(f"{release}/{arch}/packages.adb"))
            for name, group in PACKAGES.items():
                entry = entries[name]
                filename = entry["filename"]
                if filename != f"{name}-{retained['versions'][group]}.apk" or "/" in filename:
                    raise ValueError("Invalid retained package filename")
                package = fetch(f"{release}/{arch}/{filename}")
                if hashlib.sha256(package).hexdigest() != entry["sha256"]:
                    raise ValueError(f"Published package checksum mismatch: {filename}")
                (directory / filename).write_bytes(package)
    manifest = {
        "release": RELEASE,
        "sdk_release": SDK_RELEASE,
        "versions": current,
        "public_key_sha256": key_digest,
        "packages": {},
    }
    for arch in architectures:
        if not re.fullmatch(r"[a-z0-9_-]+", arch):
            raise ValueError(f"Invalid architecture: {arch}")
        directory = output / RELEASE / arch
        directory.mkdir(parents=True)
        manifest["packages"][arch] = {}
        for name, group in PACKAGES.items():
            filename = f"{name}-{current[group]}.apk"
            destination = directory / filename
            if (
                old
                and old.get("sdk_release") == SDK_RELEASE
                and arch in old["packages"]
                and old["versions"][group] == current[group]
            ):
                entry = old["packages"][arch][name]
                if entry["filename"] != filename:
                    raise ValueError("Published package filename does not match its version")
                data = fetch(f"{RELEASE}/{arch}/{filename}")
                if hashlib.sha256(data).hexdigest() != entry["sha256"]:
                    raise ValueError(f"Published package checksum mismatch: {filename}")
                destination.write_bytes(data)
            else:
                build_arch = arch if group == "updns" else "x86_64"
                matches = list((inputs / build_arch).rglob(filename))
                if len(matches) != 1:
                    raise ValueError(f"Expected one {filename} for {build_arch}, found {len(matches)}")
                shutil.copyfile(matches[0], destination)
            metadata = json.loads(subprocess.check_output(
                [apk, "adbdump", "--format", "json", str(destination)]
            ))["info"]
            expected_arch = arch if group == "updns" else "noarch"
            if (metadata["name"], metadata["version"], metadata["arch"]) != (
                name, current[group], expected_arch
            ):
                raise ValueError(f"Incorrect package metadata: {filename}")
            manifest["packages"][arch][name] = {
                "filename": filename,
                "sha256": hashlib.sha256(destination.read_bytes()).hexdigest(),
            }
        subprocess.run(
            [apk, "mkndx", "--root", str(output.resolve()), "--keys-dir", str(output.resolve()),
             "--allow-untrusted", "--sign", key, "--output", "packages.adb",
             *[entry["filename"] for entry in manifest["packages"][arch].values()]],
            cwd=directory,
            check=True,
        )
    manifest_data = (json.dumps(manifest, indent=2) + "\n").encode()
    (output / RELEASE / "manifest.json").write_bytes(manifest_data)
    (output / RELEASE / "manifest.json.sig").write_bytes(sign(manifest_data, key))
    registry_data = (json.dumps({"releases": sorted(set(releases + [RELEASE]))}, indent=2) + "\n").encode()
    (output / "releases.json").write_bytes(registry_data)
    (output / "releases.json.sig").write_bytes(sign(registry_data, key))
    generate_indexes(site)


def generate_indexes(site: Path) -> None:
    directories = [site, *sorted(path for path in site.rglob("*") if path.is_dir())]
    for directory in directories:
        files = sorted(
            path for path in directory.iterdir()
            if path.is_file() and path.name not in {"index.html", "SHA256SUMS"}
        )
        checksums = {
            path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in files
        }
        (directory / "SHA256SUMS").write_text(
            "".join(f"{digest}  {name}\n" for name, digest in checksums.items()),
            encoding="utf-8",
        )
        title = "Index of /updns/"
        relative = directory.relative_to(site).as_posix()
        if relative != ".":
            title += relative + "/"
        rows = []
        if directory != site:
            rows.append('<tr><td><a href="../">../</a></td><td></td><td></td></tr>')
        for path in sorted(directory.iterdir(), key=lambda path: (not path.is_dir(), path.name)):
            if path.name == "index.html":
                continue
            name = path.name + ("/" if path.is_dir() else "")
            href = html.escape(urllib.parse.quote(name), quote=True)
            size = "" if path.is_dir() else str(path.stat().st_size)
            rows.append(
                f'<tr><td><a href="{href}">{html.escape(name)}</a></td>'
                f'<td>{size}</td><td><code>{checksums.get(path.name, "")}</code></td></tr>'
            )
        (directory / "index.html").write_text(
            '<!doctype html>\n<html lang="en"><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width, initial-scale=1">'
            f"<title>{html.escape(title)}</title>"
            "<style>body{font-family:monospace;padding:1rem}table{border-collapse:collapse}"
            "td,th{text-align:left;padding:.4rem 1rem .4rem 0}"
            "code{overflow-wrap:anywhere}a{white-space:nowrap}</style>"
            f"<h1>{html.escape(title)}</h1>"
            "<p>Sizes are in bytes. SHA256SUMS excludes itself and index.html.</p>"
            "<table><thead><tr><th>Name</th><th>Size</th><th>SHA-256</th></tr></thead>"
            "<tbody>" + "".join(rows) + "</tbody></table></html>\n",
            encoding="utf-8",
        )


if __name__ == "__main__":
    if sys.argv[1:] == ["prepare"]:
        prepare()
    elif sys.argv[1:] == ["publish"]:
        publish()
    else:
        raise SystemExit("Usage: openwrt-repository.py prepare|publish")
