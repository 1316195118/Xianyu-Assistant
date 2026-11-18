#!/usr/bin/env python3
"""收集所有第三方依赖的 LICENSE 原文到 static/licenses/ 并生成 index.json。

构建时由 build.sh 调用；资源由 sync_webroot.py 合并进 server/webroot/licenses/，
再由 Go embed 打进单文件二进制，保证单独分发 exe 时许可声明依旧可查。
"""
import os, glob, shutil, json, re, stat, urllib.request, subprocess


def write_bytes(path, data):
    """覆盖写，先清除只读属性（Go 模块缓存里的 LICENSE 常为 0444）"""
    if os.path.exists(path):
        os.chmod(path, os.stat(path).st_mode | stat.S_IWRITE)
    with open(path, 'wb') as f:
        f.write(data)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, 'static', 'licenses')
os.makedirs(OUT, exist_ok=True)
# 零删除：同名文件直接覆盖写，不做目录清理（环境对累计删除次数有限制）

records = []
MC = subprocess.check_output(['go', 'env', 'GOMODCACHE'], cwd=os.path.join(ROOT, 'server')).decode().strip()
modlist = subprocess.check_output(['go', 'list', '-m', 'all'], cwd=os.path.join(ROOT, 'server')).decode().splitlines()[1:]

for line in modlist:
    parts = line.split()
    if len(parts) < 2:
        continue
    name, ver = parts[0], parts[1]
    d = os.path.join(MC, f'{name}@{ver}')
    cands = sorted(glob.glob(os.path.join(d, 'LICENSE*')))
    if not cands:
        continue
    src = cands[0]
    safe = re.sub(r'[^A-Za-z0-9._-]', '_', f'{name}-{ver}') + '.txt'
    with open(src, 'rb') as fi:
        write_bytes(os.path.join(OUT, safe), fi.read())
    head = open(src, encoding='utf-8', errors='ignore').read(400)
    if 'Apache License' in head:
        lic = 'Apache-2.0'
    elif 'Mozilla Public License' in head:
        lic = 'MPL-2.0'
    elif 'MIT License' in head or 'Permission is hereby granted' in head:
        lic = 'MIT'
    elif 'Redistribution and use in source and binary forms' in head:
        lic = 'BSD-3-Clause'
    else:
        lic = '见原文'
    records.append(['Go', name, ver, lic, 'licenses/' + safe])

for pkg in ['tesseract.js', 'tesseract.js-core', 'vue', 'vite', '@vitejs/plugin-vue']:
    pj = os.path.join(ROOT, 'web', 'node_modules', pkg, 'package.json')
    if not os.path.exists(pj):
        continue
    d = json.load(open(pj, encoding='utf-8'))
    ver, lic = d.get('version', ''), d.get('license', '见原文')
    cands = sorted(glob.glob(os.path.join(ROOT, 'web', 'node_modules', pkg, 'LICENSE*')))
    rel = ''
    if cands:
        safe = re.sub(r'[^A-Za-z0-9._-]', '_', f'npm-{pkg}-{ver}') + '.txt'
        with open(cands[0], 'rb') as fi:
            write_bytes(os.path.join(OUT, safe), fi.read())
        rel = 'licenses/' + safe
    records.append(['npm', pkg, ver, lic, rel])

records.append(['data', 'tessdata_fast / chi_sim.traineddata.gz', '4.0.0_fast', 'Apache-2.0', 'licenses/APACHE-2.0.txt'])

for fname, url in [
    ('APACHE-2.0.txt', 'https://www.apache.org/licenses/LICENSE-2.0.txt'),
    ('MPL-2.0.txt', 'https://www.mozilla.org/media/MPL/2.0/index.f75d2927d3c1.txt'),
]:
    try:
        txt = urllib.request.urlopen(url, timeout=25).read().decode('utf-8', 'ignore')
        open(os.path.join(OUT, fname), 'w', encoding='utf-8').write(txt)
    except Exception as e:
        print('警告：许可证全文下载失败', fname, e)

with open(os.path.join(OUT, 'index.json'), 'w', encoding='utf-8') as f:
    json.dump(records, f, ensure_ascii=False, indent=2)
print(f'许可证收集完成：{len(records)} 项 -> {OUT}')
