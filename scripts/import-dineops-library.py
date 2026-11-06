#!/usr/bin/env python3
"""Import complete product skill directories and code-defined design skills."""
import ast
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import zipfile

source = Path(sys.argv[1]).resolve()
target = Path(__file__).resolve().parents[1] / 'server/internal/templatelibrary'
(target / 'packages').mkdir(exist_ok=True)
entries = []
if (target / 'legacy_catalog.json').exists():
    entries = [e for e in json.loads((target / 'legacy_catalog.json').read_text()) if e['kind'] != 'skill' or e['id'].startswith('dineops:import:')]
def fields(name, description, content, tags):
    return dict(name=name, nameEn='', description=description, descriptionEn='', content=content, contentEn='', tags=tags, scene='', cronExpr='')
def add_skill(slug, files, origin):
    content = files['SKILL.md'][0].decode('utf-8')
    metadata = dict(re.findall(r'^(name|description):\s*(.+)$', content.split('---', 2)[1], re.M))
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
    heading = re.search(r'^#\s+(.+)$', content, re.M)
    display_name = heading.group(1) if heading else metadata['name'].strip('"\'')
    entry = dict(id='dineops:skill:' + slug, kind='skill', source='dineops', enabled=True, expectedVersion='', **fields(display_name, metadata['description'].strip('"\''), content, ['旧门道', origin]))
    entry['packageDigest'] = digest
    entry['package'] = dict(sha256=digest, size=len(raw), files=manifest)
    entries.append(entry)
for directory in sorted((source / 'skills').iterdir()):
    if not directory.is_dir() or not (directory / 'SKILL.md').is_file():
        continue
    files = {}
    for file in sorted(directory.rglob('*')):
        if file.name == '.DS_Store':
            continue
        if file.is_symlink():
            raise ValueError('Symlink requires explicit packaging: ' + str(file))
        if file.is_file():
            files[file.relative_to(directory).as_posix()] = (file.read_bytes(), bool(file.stat().st_mode & 0o111))
    add_skill(directory.name, files, '业务技能')
module = ast.parse((source / 'backend/app/design_skills.py').read_text())
for statement in module.body:
    if isinstance(statement, ast.AnnAssign) and isinstance(statement.target, ast.Name) and statement.target.id == 'DESIGN_SKILLS':
        for value in statement.value.values:
            info = {kw.arg: ast.literal_eval(kw.value) for kw in value.keywords}
            content = f'---\nname: {info["id"]}\ndescription: {info["title"]}\n---\n\n# {info["title"]}\n\n{info["prompt"]}\n'
            add_skill(info['id'], {'SKILL.md': (content.encode(), False)}, '设计技能')
if len(sys.argv) > 2:
    for record in json.loads(Path(sys.argv[2]).read_text()):
        key = hashlib.sha256(record['instructions'].encode()).hexdigest()[:20]
        identity = 'dineops:import:' + key
        entries = [e for e in entries if e['id'] != identity]
        files = {'SKILL.md': (record['instructions'].encode(), False)}
        for reference in record['references']:
            filename = reference['name']
            if '/' in filename or '\\' in filename:
                raise ValueError('Recorded reference name is not a file stem')
            if not filename.endswith('.md'):
                filename += '.md'
            files['references/' + filename] = (reference['content'].encode(), False)
        add_skill('import-' + key, files, '历史自建技能')
        entries[-1]['id'] = identity
        entries[-1]['name'] = record['name']
        entries[-1]['enabled'] = False
(target / 'legacy_catalog.json').write_text(json.dumps(entries, ensure_ascii=False, indent=2) + '\n')
skills = [e for e in entries if e['kind'] == 'skill']
print(f'Imported {len(skills)} complete skills, {sum(len(e["package"]["files"]) for e in skills)} files')
