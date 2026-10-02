# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
import json
import os
import unittest

import connector

HERE = os.path.dirname(__file__)


def load(name):
    with open(os.path.join(HERE, name)) as stream:
        return json.load(stream)


class NormalizeTest(unittest.TestCase):
    def test_maps_columns_aliases_and_links(self):
        records = connector.normalize(load('records.json'), load('mapping.json'), '2026-10-02T00:00:00Z')
        self.assertEqual(len(records), 2)
        first = records[0]
        self.assertEqual(first['type'], 'InspectionService')
        self.assertEqual(first['key'], 'vision-qa')
        self.assertEqual(first['props'], {'name': 'Vision QA (plant A)'})
        self.assertEqual(first['observed_at'], '2026-10-02T00:00:00Z')
        self.assertEqual(first['aliases'], [{'system': 'mes', 'external_id': 'vision-qa'}])
        self.assertEqual(first['links'][0]['to_key'], 'gpu-a')
        self.assertEqual(first['links'][0]['to_namespace'], 'infra')

    def test_duplicate_and_empty_keys_are_refused(self):
        mapping = load('mapping.json')
        with self.assertRaises(ValueError):
            connector.normalize([{'asset_id': 'a', 'label': 'x'}, {'asset_id': 'a', 'label': 'y'}], mapping, 't')
        with self.assertRaises(ValueError):
            connector.normalize([{'asset_id': ' ', 'label': 'x'}], mapping, 't')

    def test_missing_column_is_an_error_not_a_guess(self):
        with self.assertRaises(KeyError):
            connector.normalize([{'label': 'x'}], load('mapping.json'), 't')

    def test_blank_values_are_dropped(self):
        records = connector.normalize([{'asset_id': 'a', 'label': '', 'cluster': ''}], load('mapping.json'), 't')
        self.assertEqual(records[0]['props'], {})
        self.assertNotIn('links', records[0])

    def test_remote_http_is_refused(self):
        with self.assertRaises(ValueError):
            connector.send('http://example.com', 'alpha', 'tok', 'mes', [])


if __name__ == '__main__':
    unittest.main()
