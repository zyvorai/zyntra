#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
"""Normalize an upstream JSON export into Zyntra ontology records.

Stdlib only. Dry-run by default: it prints the records. With --send it pushes
them to POST /api/v1/ontology/ingest/{tenant}, which needs ZYNTRA_INGEST_TOKEN
and an `ingest_tenants` grant for that tenant in the policy file.

The mapping is written by an administrator; it names columns, never commands.
Observation time must come from the upstream system (--observed-at), so stale
data is never labelled as fresh.
"""
import argparse
import datetime
import json
import os
import sys
import urllib.parse
import urllib.request

MAX_RECORDS = 5000


def normalize(rows, mapping, observed_at):
    """Turn upstream rows into ontology records using the mapping."""
    records, seen = [], set()
    for number, row in enumerate(rows, 1):
        key = str(row[mapping['key_column']]).strip()
        if not key:
            raise ValueError('row %d: empty key' % number)
        if key in seen:
            raise ValueError('row %d: duplicate key %s' % (number, key))
        seen.add(key)
        record = {
            'type': mapping['object_type'],
            'namespace': mapping['namespace'],
            'key': key,
            'props': {prop: row[column] for prop, column in mapping['props'].items()
                      if row.get(column) not in (None, '')},
            'observed_at': observed_at,
            'source_id': 'row:%d' % number,
            'transform': ['column-map'],
        }
        aliases = [{'system': a['system'], 'external_id': str(row[a['column']])}
                   for a in mapping.get('aliases', []) if row.get(a['column']) not in (None, '')]
        if aliases:
            record['aliases'] = aliases
        links = [{'type': l['type'], 'to_type': l['to_type'],
                  'to_namespace': l.get('to_namespace', mapping['namespace']),
                  'to_key': str(row[l['column']])}
                 for l in mapping.get('links', []) if row.get(l['column']) not in (None, '')]
        if links:
            record['links'] = links
        records.append(record)
    if len(records) > MAX_RECORDS:
        raise ValueError('more than %d records; split the export' % MAX_RECORDS)
    return records


def send(url, tenant, token, source, records):
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != 'https' and parsed.hostname not in ('localhost', '127.0.0.1', '::1'):
        raise ValueError('remote delivery requires HTTPS')
    target = url.rstrip('/') + '/api/v1/ontology/ingest/' + urllib.parse.quote(tenant, safe='')
    body = json.dumps({'source': source, 'records': records}).encode()
    request = urllib.request.Request(target, data=body, method='POST', headers={
        'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        # Never forward the token through a redirect.
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    with urllib.request.build_opener(NoRedirect).open(request, timeout=30) as response:
        return response.read().decode()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, help='JSON file: a list of row objects')
    parser.add_argument('--mapping', required=True)
    parser.add_argument('--tenant', required=True, help='tenant to write into ("default" for none)')
    parser.add_argument('--source', required=True)
    parser.add_argument('--observed-at', required=True, help='upstream observation time, ISO 8601 UTC')
    parser.add_argument('--url', default='http://127.0.0.1:8080')
    parser.add_argument('--send', action='store_true')
    parser.add_argument('--jsonl', action='store_true', help='print one record per line (for an exec connector)')
    args = parser.parse_args(argv)
    datetime.datetime.fromisoformat(args.observed_at.replace('Z', '+00:00'))
    with open(args.input) as stream:
        rows = json.load(stream)
    with open(args.mapping) as stream:
        mapping = json.load(stream)
    records = normalize(rows, mapping, args.observed_at)
    if args.send:
        token = os.environ.get('ZYNTRA_INGEST_TOKEN')
        if not token:
            parser.error('--send requires ZYNTRA_INGEST_TOKEN')
        print(send(args.url, args.tenant, token, args.source, records))
    elif args.jsonl:
        for record in records:
            print(json.dumps(record))
    else:
        print(json.dumps({'source': args.source, 'records': records}, indent=2))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError) as error:
        print('connector: %s' % error, file=sys.stderr)
        sys.exit(1)
