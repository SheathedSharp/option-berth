"""Pure local Markdown checker fixtures; no network or product process."""
from pathlib import Path
import tempfile
import unittest
from check_doc_links import anchors, check_file, main


class LinkTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(); self.addCleanup(temp.cleanup)
        self.root = Path(temp.name); self.page = self.root / "guide.md"

    def test_unicode_explicit_and_duplicate_fragments(self):
        self.page.write_text('# 运行宿主\n## A & B\n## A & B\n<a id="old-entry"></a>\n')
        self.assertTrue({'运行宿主','a--b','a--b-1','old-entry'} <= anchors(self.page))

    def test_broken_and_valid_fragment_use_actual_heading_path(self):
        self.page.write_text('# Guide\n[valid](#guide) [bad](#absent)\n')
        self.assertEqual(1,len(check_file(self.page,self.root)))
        self.assertIn('missing fragment',check_file(self.page,self.root)[0])

    def test_examples_are_not_links(self):
        self.page.write_text('# Guide\n```md\n[example](missing.md)\n```\n[real](missing.md)\n')
        self.assertEqual(1,len(check_file(self.page,self.root)))

    def test_percent_encoded_unicode_fragment(self):
        self.page.write_text('# 中文\n[link](#%E4%B8%AD%E6%96%87)\n')
        self.assertEqual([],check_file(self.page,self.root))

    def test_reference_and_html_links(self):
        self.page.write_text('# Guide\n[missing]: absent.md\n<a href="missing.md">x</a>\n')
        self.assertEqual(2,len(check_file(self.page,self.root)))

    def test_actual_corpus_and_readme_pair(self):
        self.assertEqual(0, main())


if __name__ == '__main__':
    unittest.main()
