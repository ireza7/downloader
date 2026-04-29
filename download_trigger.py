import sys, os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '_vendor'))

import os
import re
import time
from pathlib import Path
from download import download_all, create_zip

TRIGGER_FILE = "trigger_download.txt"
DOWNLOAD_DIR = "downloads"


def read_trigger():
    with open(TRIGGER_FILE, 'r', encoding='utf-8') as f:
        return f.readlines()


def parse_lines(lines):
    simple_comment = "# اگر لینک های خود را در زیر این کامنت وارد کنید فایل ها بدون تغییر به صورت ساده ذخیره میشوند"
    zipall_comment = "# اگر لینک هارا زیر این کامنت وارد کنید همه فایل ها در یک فایل فشرده زیپ ذخیره میشوند"
    zipeach_comment = "# اگر لینک هارا زیر این کامنت وارد کنید هر فایل به صورت یک فایل فشرده زیپ ذخیره میشوند"

    mode = None
    simple = []
    zipall = []
    zipeach = []

    url_re = re.compile(r'https?://[^\s]+')

    for raw_line in lines:
        line = raw_line.strip()
        if not line or line.startswith('#'):
            if 'بدون تغییر' in line or line == simple_comment:
                mode = 'simple'
            elif 'همه فایل ها در یک فایل' in line or line == zipall_comment:
                mode = 'zipall'
            elif 'هر فایل به صورت یک فایل' in line or line == zipeach_comment:
                mode = 'zipeach'
            continue

        urls = url_re.findall(line)
        if mode == 'simple':
            simple.extend(urls)
        elif mode == 'zipall':
            zipall.extend(urls)
        elif mode == 'zipeach':
            zipeach.extend(urls)
        else:
            simple.extend(urls)

    return simple, zipall, zipeach


def save_files(files_dict, output_dir):
    output_dir.mkdir(parents=True, exist_ok=True)
    for fname, data in files_dict.items():
        output_path = output_dir / fname
        output_path.write_bytes(data)
        print(f"Saved: {output_path}")


def main():
    lines = read_trigger()
    simple_urls, zipall_urls, zipeach_urls = parse_lines(lines)

    output_dir = Path(DOWNLOAD_DIR)

    if simple_urls:
        print(f"Simple mode: {simple_urls}")
        files = download_all(simple_urls)
        if files:
            save_files(files, output_dir)
        else:
            print("No files downloaded (simple).")

    if zipall_urls:
        print(f"ZipAll mode: {zipall_urls}")
        files = download_all(zipall_urls)
        if files:
            zip_data = create_zip(files)
            zip_name = f"archive_{int(time.time())}.zip"
            (output_dir / zip_name).write_bytes(zip_data)
            print(f"ZipAll saved: {zip_name}")
        else:
            print("No files downloaded (zipall).")

    if zipeach_urls:
        print(f"ZipEach mode: {zipeach_urls}")
        files = download_all(zipeach_urls)
        suffix = int(time.time())
        for fname, data in files.items():
            stem = Path(fname).stem
            zip_name = f"{stem}_{suffix}.zip"
            zip_data = create_zip({fname: data})
            (output_dir / zip_name).write_bytes(zip_data)
            print(f"ZipEach saved: {zip_name}")

    print("Trigger processing finished.")


if __name__ == "__main__":
    main()