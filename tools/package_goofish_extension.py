#!/usr/bin/env python3
"""把浏览器扩展打包为网页可直接下载的 ZIP。

Chrome / Edge 不允许普通网页直接安装未上架的扩展，因此网页提供扩展包下载，
用户解压后运行安装脚本，并在浏览器扩展页加载解压目录。
"""
import os
import hashlib
import json
import zipfile


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SOURCE = os.path.join(ROOT, "tools", "goofish_extension")
OUTPUT_DIR = os.path.join(ROOT, "static", "downloads")
OUTPUT = os.path.join(OUTPUT_DIR, "goofish-extension.zip")
METADATA = os.path.join(OUTPUT_DIR, "goofish-extension-version.json")
FILES = (
    "manifest.json",
    "background.js",
    "popup.html",
    "popup.css",
    "popup.js",
    "icons/icon16.png",
    "icons/icon32.png",
    "icons/icon48.png",
    "icons/icon128.png",
    "options.html",
    "options.css",
    "options.js",
    "README.md",
    "install_goofish_extension.bat",
)


def main():
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    tmp = OUTPUT + ".tmp"
    with zipfile.ZipFile(tmp, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for name in FILES:
            source = os.path.join(SOURCE, name)
            if not os.path.isfile(source):
                raise SystemExit(f"缺少扩展文件: {source}")
            info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o644 << 16
            with open(source, "rb") as source_file:
                archive.writestr(info, source_file.read())
    os.replace(tmp, OUTPUT)
    with open(os.path.join(SOURCE, "manifest.json"), encoding="utf-8") as manifest_file:
        version = json.load(manifest_file)["version"]
    with open(OUTPUT, "rb") as package_file:
        digest = hashlib.sha256(package_file.read()).hexdigest()
    metadata = {
        "version": version,
        "download_path": "/downloads/goofish-extension.zip",
        "sha256": digest,
    }
    with open(METADATA, "w", encoding="utf-8") as metadata_file:
        json.dump(metadata, metadata_file, ensure_ascii=False, indent=2)
        metadata_file.write("\n")
    print(f"扩展包已生成：{OUTPUT}")
    print(f"版本清单已生成：{METADATA}")


if __name__ == "__main__":
    main()
