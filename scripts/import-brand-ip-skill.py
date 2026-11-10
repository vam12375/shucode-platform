#!/usr/bin/env python3
"""把 docs/template-library/curated-skills/brand-ip-director 打成单文件 Skill 包并写入精选目录。

规则与 import-restaurant-video-kit.py 一致：ZIP 内文件排序、固定时间戳、权限 0644，包名为内容 sha256。
media 的制作单快照只接受“单个非可执行 SKILL.md”的纯指令包，所以这里只打包 SKILL.md 一个文件。
重跑是幂等的：同 id 的旧条目与不再被引用的旧包会被替换掉。

用法（仓库根目录）：python3 -I scripts/import-brand-ip-skill.py
"""
import hashlib
import io
import json
from pathlib import Path
import re
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'docs/template-library/curated-skills/brand-ip-director'
TARGET = ROOT / 'server/internal/templatelibrary'
SKILL_ID = 'curated:brand-ip-director'
TAGS = ['视频制作', '本次制作单', '品牌IP']

catalog = TARGET / 'curated_catalog.json'
original = catalog.read_text(encoding='utf-8')
entries = json.loads(original)
if json.dumps(entries, ensure_ascii=False, indent=2) + '\n' != original:
    raise ValueError('curated_catalog.json is not in canonical form; refusing to rewrite it')

files = [p for p in sorted(SOURCE.rglob('*')) if p.is_file() and p.name != '.DS_Store']
if [p.relative_to(SOURCE).as_posix() for p in files] != ['SKILL.md']:
    raise ValueError('brand-ip-director must be a single SKILL.md (recipe skills are instruction-only)')
raw_skill = files[0].read_bytes()
content = raw_skill.decode('utf-8')

parts = content.split('---', 2)
if len(parts) != 3 or parts[0]:
    raise ValueError('Missing front matter')
description = re.search(r'^description:[ \t]*(.+)$', parts[1], re.M).group(1).strip()
heading = re.search(r'^#\s+(.+)$', content, re.M).group(1)

data = io.BytesIO()
with zipfile.ZipFile(data, 'w', zipfile.ZIP_DEFLATED) as archive:
    header = zipfile.ZipInfo('SKILL.md', (2026, 1, 1, 0, 0, 0))
    header.compress_type = zipfile.ZIP_DEFLATED
    header.external_attr = 0o100644 << 16
    archive.writestr(header, raw_skill)
package = data.getvalue()
digest = hashlib.sha256(package).hexdigest()

entry = dict(
    id=SKILL_ID, kind='skill', source='curated', enabled=True, expectedVersion='',
    name=heading, nameEn=description,
    description='门道原创适配；配方关联使用，仅对当前制作单生效。',
    descriptionEn='Mendao-authored, instruction-only adaptation scoped to one production.',
    content=content, contentEn='', tags=TAGS, scene='', cronExpr='',
    packageDigest=digest,
    package=dict(sha256=digest, size=len(package), files=[
        dict(path='SKILL.md', size=len(raw_skill), sha256=hashlib.sha256(raw_skill).hexdigest(), executable=False)]),
)

stale = {e['packageDigest'] for e in entries if e['id'] == SKILL_ID and e.get('packageDigest')}
entries = [e for e in entries if e['id'] != SKILL_ID]
# 追加在末尾：后台列表和 store 测试按下标取条目，不挪动已有条目的位置。
entries.append(entry)

legacy = json.loads((TARGET / 'legacy_catalog.json').read_text(encoding='utf-8'))
identities = [e['id'] for e in entries + legacy]
if len(identities) != len(set(identities)):
    raise ValueError('Duplicate catalog identity')

(TARGET / 'packages').mkdir(exist_ok=True)
(TARGET / 'packages' / (digest + '.zip')).write_bytes(package)
referenced = {e['packageDigest'] for e in entries + legacy if e.get('packageDigest')}
for old in stale - referenced:
    (TARGET / 'packages' / (old + '.zip')).unlink(missing_ok=True)
catalog.write_text(json.dumps(entries, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(f'Imported {SKILL_ID}: package {digest[:12]}…, {len(package)} bytes; curated entries {len(entries)}')
