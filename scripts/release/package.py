#!/usr/bin/env python3
"""Cross-compile the supported standalone release archives."""

import argparse
import gzip
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parents[2]
TARGETS = ("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64")
VERSION = re.compile(
    r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
    r"(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
    r"(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?"
    r"|ci-[1-9][0-9]*-[0-9a-f]{7,40}"
)


def validate_version(value):
    if not VERSION.fullmatch(value):
        raise ValueError("version must be X.Y.Z, X.Y.Z-prerelease, or ci-RUN-SHA")
    return value


def write_archive(destination, stem, binary):
    """Write a portable archive with stable metadata and executable permissions."""
    with destination.open("wb") as output:
        with gzip.GzipFile(filename="", mode="wb", fileobj=output, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                directory = tarfile.TarInfo(stem)
                directory.type, directory.mode = tarfile.DIRTYPE, 0o755
                archive.addfile(directory)
                files = [(binary, "richt", 0o755), (ROOT / "README.md", "README.md", 0o644)]
                if (ROOT / "LICENSE").is_file():
                    files.append((ROOT / "LICENSE", "LICENSE", 0o644))
                for source, name, mode in files:
                    member = tarfile.TarInfo(f"{stem}/{name}")
                    member.size, member.mode = source.stat().st_size, mode
                    with source.open("rb") as content:
                        archive.addfile(member, content)


def package(version, output_dir):
    validate_version(version)
    output_dir = Path(output_dir).resolve()
    # Stage all builds before publishing any archive, so a failed build leaves
    # no partial release set in the output directory.
    with tempfile.TemporaryDirectory(prefix="richt-release-") as temporary:
        stage = Path(temporary)
        for target in TARGETS:
            goos, goarch = target.split("-")
            binary = stage / f"richt-{target}"
            environment = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
            subprocess.run(
                [os.environ.get("GO", "go"), "build", "-trimpath", "-ldflags",
                 f"-X github.com/subuk/richt/cli.Version={version}", "-o", str(binary), "."],
                cwd=ROOT, env=environment, check=True,
            )
            stem = f"richt-{version}-{target}"
            write_archive(stage / f"{stem}.tar.gz", stem, binary)
        output_dir.mkdir(parents=True, exist_ok=True)
        for archive in sorted(stage.glob("*.tar.gz")):
            destination = output_dir / archive.name
            # Copy through the destination filesystem before atomic replacement.
            with tempfile.NamedTemporaryFile(dir=output_dir, prefix=".richt-", delete=False) as output:
                pending = Path(output.name)
                try:
                    with archive.open("rb") as source:
                        shutil.copyfileobj(source, output)
                    output.close()
                    pending.replace(destination)
                finally:
                    pending.unlink(missing_ok=True)
            print(destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path, default=Path("dist"))
    args = parser.parse_args()
    try:
        package(args.version, args.output_dir)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"package: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
