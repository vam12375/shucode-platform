#!/usr/bin/env python3
"""Import the restaurant video kit: its skills as complete packages, its story and dish-insert templates as text templates.

Usage: python3 -I scripts/import-restaurant-video-kit.py <path to restaurant-video-kit>
"""
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import zipfile

SUITE = '餐饮一键成片'
source = Path(sys.argv[1]).resolve()
target = Path(__file__).resolve().parents[1] / 'server/internal/templatelibrary'
(target / 'packages').mkdir(exist_ok=True)
catalog = target / 'curated_catalog.json'
original = catalog.read_text()
entries = json.loads(original)
if json.dumps(entries, ensure_ascii=False, indent=2) + '\n' != original:
    raise ValueError('curated_catalog.json is not in canonical form; refusing to rewrite it')
# Re-running replaces the previous import, so removed or changed kit files do not leave stale entries or packages behind.
stale = {e['packageDigest'] for e in entries if SUITE in e['tags'] and e.get('packageDigest')}
entries = [e for e in entries if SUITE not in e['tags']]
imported = []
def fields(name, description, content, tags):
    return dict(name=name, nameEn='', description=description, descriptionEn='', content=content, contentEn='', tags=tags, scene='', cronExpr='')
def front_matter(content):
    parts = content.split('---', 2)
    if len(parts) != 3 or parts[0]:
        raise ValueError('Missing front matter')
    return {key: value.strip() for key, value in re.findall(r'^(\w+):[ \t]*(.+)$', parts[1], re.M)}
def display_name(content):
    heading = re.search(r'^#\s+(.+)$', content, re.M).group(1)
    return heading if heading.startswith(SUITE) else f'{SUITE} · {heading}'
def add_skill(directory):
    files = {}
    for file in sorted(directory.rglob('*')):
        relative = file.relative_to(directory)
        if {'.DS_Store', '__pycache__'} & set(relative.parts):
            continue
        if file.is_symlink():
            raise ValueError('Symlink requires explicit packaging: ' + str(file))
        if file.is_file():
            files[relative.as_posix()] = (file.read_bytes(), bool(file.stat().st_mode & 0o111))
    content = files['SKILL.md'][0].decode('utf-8')
    data = io.BytesIO()
    manifest = []
    with zipfile.ZipFile(data, 'w', zipfile.ZIP_DEFLATED) as archive:
        for name, (raw, executable) in sorted(files.items()):
            header = zipfile.ZipInfo(name, (2026, 1, 1, 0, 0, 0))
            header.compress_type = zipfile.ZIP_DEFLATED
            header.external_attr = (0o100755 if executable else 0o100644) << 16
            archive.writestr(header, raw)
            manifest.append(dict(path=name, size=len(raw), sha256=hashlib.sha256(raw).hexdigest(), executable=executable))
    raw = data.getvalue()
    digest = hashlib.sha256(raw).hexdigest()
    (target / 'packages' / (digest + '.zip')).write_bytes(raw)
    entry = dict(id='curated:' + directory.name, kind='skill', source='curated', enabled=True, expectedVersion='', **fields(display_name(content), front_matter(content)['description'], content, [SUITE, '视频制作', '餐饮探店']))
    entry['packageDigest'] = digest
    entry['package'] = dict(sha256=digest, size=len(raw), files=manifest)
    imported.append(entry)
def add_template(slug, name, description, content, tags):
    imported.append(dict(id='curated:template:' + slug, kind='template', source='curated', enabled=True, expectedVersion='', **fields(name, description, content, tags)))
for directory in sorted((source / 'skills').iterdir()):
    if directory.is_dir() and (directory / 'SKILL.md').is_file():
        add_skill(directory)
# Story templates keep their original Markdown, front matter included.
for file in sorted((source / 'skills/restaurant-story-planner/references/stories').glob('*.md')):
    content = file.read_text(encoding='utf-8')
    meta = front_matter(content)
    summary = re.search(r'^\*\*一句话\*\*：(.+)$', content, re.M).group(1).replace('**', '')
    durations = '/'.join(re.findall(r'\d+', meta['durations']))
    description = f'{summary}\n适用 {durations} 秒竖屏；调性：{meta["tone"]}；含幕表与接缝表，配合「{SUITE}」Skills 使用。'
    add_template(meta['id'], '探店剧情 · ' + meta['name'], description, content, [SUITE, '剧情模板', '9:16'])
insert = (source / 'skills/seedance-prompt-composer/references/dish-insert-template.md').read_text(encoding='utf-8')
purpose = re.search(r'^用途：(.+)$', insert, re.M).group(1).replace('**', '')
add_template('dish-insert', display_name(insert), f'{purpose}\n试拍与 insert 模式使用，配合「{SUITE}」Skills 使用。', insert, [SUITE, '视频提示词模板', '9:16'])
entries += imported
legacy = json.loads((target / 'legacy_catalog.json').read_text())
identities = [e['id'] for e in entries + legacy]
if len(identities) != len(set(identities)):
    raise ValueError('Duplicate catalog identity')
referenced = {e['packageDigest'] for e in entries + legacy if e.get('packageDigest')}
for digest in stale - referenced:
    (target / 'packages' / (digest + '.zip')).unlink(missing_ok=True)
catalog.write_text(json.dumps(entries, ensure_ascii=False, indent=2) + '\n')
skills = [e for e in imported if e['kind'] == 'skill']
print(f'Imported {len(skills)} skills, {sum(len(e["package"]["files"]) for e in skills)} files, {len(imported) - len(skills)} templates')
