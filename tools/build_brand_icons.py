#!/usr/bin/env python3
"""从品牌 512px 母图生成 favicon、Apple Touch Icon 与 PWA 图标。

母图来源：web/public/brand/icon-source.svg
渲染为 web/public/icon-512.png 后运行本脚本即可稳定生成其余尺寸。
"""

from pathlib import Path

from PIL import Image


ROOT = Path(__file__).resolve().parent.parent
PUBLIC = ROOT / "web" / "public"
SOURCE = PUBLIC / "icon-512.png"


def main() -> None:
    if not SOURCE.is_file():
        raise SystemExit(f"缺少 512px 品牌母图：{SOURCE}")

    with Image.open(SOURCE) as image:
        icon = image.convert("RGBA")
        if icon.size != (512, 512):
            raise SystemExit(f"品牌母图尺寸必须是 512×512，当前为 {icon.size[0]}×{icon.size[1]}")

        for name, size in (
            ("apple-touch-icon.png", 180),
            ("icon-192.png", 192),
        ):
            icon.resize((size, size), Image.Resampling.LANCZOS).save(
                PUBLIC / name,
                format="PNG",
                optimize=True,
            )

        icon.save(
            PUBLIC / "favicon.ico",
            format="ICO",
            sizes=[(16, 16), (32, 32), (48, 48)],
        )

    print("品牌图标已生成：favicon.ico、apple-touch-icon.png、icon-192.png、icon-512.png")


if __name__ == "__main__":
    main()
