"""Update version values in the existing single-section AMD64 resource object.
Preserve all icons, resource identifiers and language records; require an exclusive backup.
This edits source resources only. Build and promotion remain controlled by build.ps1.
"""
import argparse
import pathlib
import re
import struct


def align(value):
    return (value + 3) & ~3


def block(data, offset=0):
    size, value_size, kind = struct.unpack_from('<HHH', data, offset)
    end = offset + size
    if size < 6 or end > len(data):
        raise ValueError('Invalid VERSIONINFO block')
    key_end = offset + 6
    while data[key_end:key_end + 2] != b'\0\0':
        key_end += 2
        if key_end >= end:
            raise ValueError('Unterminated VERSIONINFO key')
    key = data[offset + 6:key_end].decode('utf-16le')
    value_start = align(key_end + 2)
    value_end = value_start + value_size * (2 if kind else 1)
    if value_end > end:
        raise ValueError('Invalid VERSIONINFO value')
    children = []
    cursor = align(value_end)
    while cursor + 6 <= end:
        child, length = block(data, cursor)
        children.append(child)
        cursor = align(cursor + length)
    return {'key': key, 'kind': kind, 'value': data[value_start:value_end], 'children': children}, size


def pack_block(node):
    key = (node['key'] + '\0').encode('utf-16le')
    result = bytearray(struct.pack('<HHH', 0, len(node['value']) // (2 if node['kind'] else 1), node['kind']) + key)
    result.extend(b'\0' * (align(len(result)) - len(result)))
    result.extend(node['value'])
    for child in node['children']:
        result.extend(b'\0' * (align(len(result)) - len(result)))
        result.extend(pack_block(child))
    struct.pack_into('<H', result, 0, len(result))
    return bytes(result)


def update_version(payload, version):
    node, _ = block(payload)
    if node['key'] != 'VS_VERSION_INFO' or len(node['value']) != 52:
        raise ValueError('Unsupported VERSIONINFO root')
    fixed = bytearray(node['value'])
    if struct.unpack_from('<I', fixed)[0] != 0xFEEF04BD:
        raise ValueError('Invalid fixed version signature')
    major, minor, patch = map(int, version.split('.'))
    if max(major, minor, patch) > 65535:
        raise ValueError('Version component exceeds WORD')
    struct.pack_into('<IIII', fixed, 8, (major << 16) | minor, patch << 16, (major << 16) | minor, patch << 16)
    node['value'] = bytes(fixed)
    changed = []
    def walk(item):
        if item['key'] in ('FileVersion', 'ProductVersion'):
            item['value'] = (version + '\0').encode('utf-16le')
            changed.append(item['key'])
        for child in item['children']:
            walk(child)
    walk(node)
    if sorted(changed) != ['FileVersion', 'ProductVersion']:
        raise ValueError('Missing or duplicate version strings')
    return pack_block(node)


def main():
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument('--version', required=True)
    args.add_argument('--backup', required=True)
    options = args.parse_args()
    if not re.fullmatch(r'\d+\.\d+\.\d+', options.version):
        raise ValueError('Expected semantic version')
    root = pathlib.Path(__file__).resolve().parent.parent
    target = root / 'cmd/mihomolite/rsrc_windows_amd64.syso'
    backup = (root / options.backup).absolute()
    allowed = root / '.cache/tests'
    if '..' in backup.parts or not backup.is_relative_to(allowed) or backup.exists():
        raise ValueError('Backup must be a new file beneath .cache/tests')
    for candidate in [target, *target.parents, backup, *backup.parents]:
        if candidate.exists() and (candidate.is_symlink() or candidate.is_junction()):
            raise ValueError('Reparse paths are not allowed')
    original = target.read_bytes()
    machine, count, _, symbol_at, symbols, optional, _ = struct.unpack_from('<HHLLLHH', original)
    if machine != 0x8664 or count != 1 or optional != 0 or original[20:28].rstrip(b'\0') != b'.rsrc':
        raise ValueError('Expected one AMD64 .rsrc COFF section')
    size, raw_at, reloc_at = struct.unpack_from('<III', original, 36)
    relocations = struct.unpack_from('<H', original, 52)[0]
    section = bytearray(original[raw_at:raw_at + size])
    leaves = []
    def directory(offset, depth=0, resource_type=None):
        if depth > 2:
            raise ValueError('Unsupported resource directory depth')
        named, numbered = struct.unpack_from('<HH', section, offset + 12)
        for index in range(named + numbered):
            name, child = struct.unpack_from('<II', section, offset + 16 + index * 8)
            kind = name if depth == 0 else resource_type
            if child & 0x80000000:
                directory(child & 0x7FFFFFFF, depth + 1, kind)
            else:
                data_at, length = struct.unpack_from('<II', section, child)
                if data_at + length > size:
                    raise ValueError('Resource payload exceeds section')
                leaves.append((child, data_at, length, kind))
    directory(0)
    replacements = sorted((start, length, update_version(bytes(section[start:start + length]), options.version)) for _, start, length, kind in leaves if kind == 16)
    if not replacements or any(entry >= replacements[0][0] for entry, _, _, _ in leaves):
        raise ValueError('Unsupported resource data placement')
    def translated(offset):
        return offset + sum(align(len(value)) - align(length) for start, length, value in replacements if start < offset)
    updated = bytearray()
    cursor = 0
    for start, length, value in replacements:
        updated.extend(section[cursor:start])
        updated.extend(value)
        updated.extend(b'\0' * (align(len(value)) - len(value)))
        cursor = start + align(length)
    updated.extend(section[cursor:])
    for entry, start, length, kind in leaves:
        new_size = next((len(value) for old, _, value in replacements if old == start), length)
        struct.pack_into('<II', updated, entry, translated(start), new_size)
    delta = len(updated) - size
    result = bytearray(original[:raw_at]) + updated + original[raw_at + size:]
    struct.pack_into('<I', result, 8, symbol_at + delta)
    struct.pack_into('<I', result, 36, len(updated))
    struct.pack_into('<I', result, 44, reloc_at + delta)
    for index in range(relocations):
        position = reloc_at + delta + index * 10
        old_offset = struct.unpack_from('<I', result, position)[0]
        struct.pack_into('<I', result, position, translated(old_offset))
    # The existing section symbol has value zero; reject layouts needing other relocation changes.
    if symbols != 1 or struct.unpack_from('<I', result, symbol_at + delta + 8)[0] != 0:
        raise ValueError('Unsupported COFF symbols')
    backup.parent.mkdir(parents=True, exist_ok=True)
    with backup.open('xb') as destination:
        destination.write(original)
    target.write_bytes(result)
    print(f'Resource version {options.version}; {len(replacements)} VERSIONINFO; {len(result)} bytes; original backed up')


if __name__ == '__main__':
    main()
