#!/usr/bin/env python3
"""扫描公开静态图片，把较大的 PNG/JPEG 转换为 WebP。

默认只预览，不修改文件。确认结果后使用 --write。
"""

from __future__ import annotations

import argparse
from pathlib import Path

from PIL import Image


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_DIRS = (ROOT / "web" / "public", ROOT / "static")
EXCLUDED_NAMES = {
    "favicon.ico",
    "apple-touch-icon.png",
    "icon-192.png",
    "icon-512.png",
}
PHOTO_SUFFIXES = {".png", ".jpg", ".jpeg"}


def human_size(size: int) -> str:
    if size >= 1024 * 1024:
        return f"{size / (1024 * 1024):.1f} MiB"
    return f"{size / 1024:.0f} KiB"


def output_path(source: Path) -> Path:
    return source.with_suffix(".webp")


def should_skip(path: Path, threshold: int) -> bool:
    if not path.is_file():
        return True
    if path.name in EXCLUDED_NAMES:
        return True
    if path.suffix.lower() not in PHOTO_SUFFIXES:
        return True
    return path.stat().st_size < threshold


def optimize(source: Path, args: argparse.Namespace) -> tuple[int, int] | None:
    target = output_path(source)
    if target.exists() and not args.force:
        print(f"跳过   {source.relative_to(ROOT)} -> {target.name}（已存在）")
        return None

    with Image.open(source) as image:
        image.load()
        original_width, original_height = image.size
        if image.mode not in ("RGB", "RGBA"):
            has_alpha = "A" in image.getbands() or "transparency" in image.info
            image = image.convert("RGBA" if has_alpha else "RGB")

        if args.max_width and image.width > args.max_width:
            ratio = args.max_width / image.width
            resized = (args.max_width, max(1, round(image.height * ratio)))
            image = image.resize(resized, Image.Resampling.LANCZOS)

        before = source.stat().st_size
        if not args.write:
            print(
                f"可转换 {source.relative_to(ROOT)}  {original_width}x{original_height} "
                f"{human_size(before)} -> {target.name}"
            )
            return before, before

        image.save(target, format="WEBP", quality=args.quality, method=6)

    after = target.stat().st_size
    saved = max(0, before - after)
    percent = saved / before * 100 if before else 0
    print(
        f"已转换 {source.relative_to(ROOT)} -> {target.relative_to(ROOT)}  "
        f"{human_size(before)} -> {human_size(after)}（节省 {percent:.1f}%）"
    )
    return before, after


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", action="append", default=[], help="额外扫描目录；可重复")
    parser.add_argument("--threshold-kib", type=int, default=200, help="只处理大于该大小的图片")
    parser.add_argument("--max-width", type=int, default=2000, help="最大宽度，0 表示不缩放")
    parser.add_argument("--quality", type=int, default=82, choices=range(40, 100), metavar="40-99")
    parser.add_argument("--write", action="store_true", help="实际写入 WebP；默认只预览")
    parser.add_argument("--force", action="store_true", help="覆盖已存在的 WebP")
    args = parser.parse_args()

    roots = list(DEFAULT_DIRS)
    roots.extend(Path(value).resolve() for value in args.dir)
    threshold = args.threshold_kib * 1024
    files = sorted(
        path
        for root in roots
        if root.exists()
        for path in root.rglob("*")
        if not should_skip(path, threshold)
    )

    if not files:
        print(f"没有发现大于 {args.threshold_kib} KiB 的 PNG/JPEG 图片。")
        return

    total_before = 0
    total_after = 0
    for source in files:
        result = optimize(source, args)
        if result:
            total_before += result[0]
            total_after += result[1]

    if args.write:
        saved = max(0, total_before - total_after)
        print(f"完成：处理 {len(files)} 张，合计节省 {human_size(saved)}。原图已保留。")
    else:
        print(f"预览：{len(files)} 张待转换；加 --write 才实际写入。")


if __name__ == "__main__":
    main()
