import sys, os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '_vendor'))

import re
import time
import requests
import zipfile
import io
import threading
from urllib.parse import urlparse, unquote
from pathlib import Path

session = requests.Session()
session.headers.update({'User-Agent': 'GitHubDownloader/1.0'})
session.max_redirects = 10
TIMEOUT = 60
MAX_CHUNKS = 4


def extract_filename(url_str, content_disposition=None):
    # استخراج از Content-Disposition
    cd_name = None
    if content_disposition:
        match = re.search(r'filename\*?=(?:UTF-8\'\')?["\']?([^";\'\s]+)', content_disposition)
        if match:
            cd_name = unquote(match.group(1))

    # استخراج نام از مسیر URL (بدون کوئری)
    parsed = urlparse(url_str)
    url_name = unquote(Path(parsed.path).name) if parsed.path else None
    if url_name in ('', '.'):
        url_name = None

    # تشخیص UUID (مثل 33ae66a2-...)
    uuid_re = re.compile(r'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$')
    def is_uuid(n):
        return bool(uuid_re.match(n))

    def is_valid_name(n):
        if not n:
            return False
        if is_uuid(n):
            return False
        return True

    # اولویت: CD معتبر > URL معتبر > CD خام > URL خام > پیش‌فرض
    if is_valid_name(cd_name):
        return cd_name
    if is_valid_name(url_name):
        return url_name
    if cd_name:
        return cd_name
    if url_name:
        return url_name
    return "downloaded_file"


def download_file(url, max_chunks=MAX_CHUNKS):
    try:
        head = session.head(url, timeout=10, allow_redirects=True)
    except Exception as e:
        raise Exception(f"HEAD failed: {e}")

    final_url = head.url
    cd = head.headers.get('Content-Disposition', '')
    filename = extract_filename(final_url, cd)

    content_length = head.headers.get('Content-Length')
    if content_length and content_length.isdigit():
        content_length = int(content_length)
    else:
        content_length = None

    accept_ranges = head.headers.get('Accept-Ranges', '')
    if accept_ranges == 'bytes' and content_length and content_length > 1024 * 10:
        try:
            return download_chunked(url, content_length, filename, max_chunks)
        except Exception:
            pass

    resp = session.get(url, stream=True, timeout=TIMEOUT)
    resp.raise_for_status()
    data = resp.content
    final_cd = resp.headers.get('Content-Disposition', '')
    if final_cd:
        filename = extract_filename(resp.url, final_cd)
    return data, filename


def download_chunked(url, total_size, filename, max_chunks):
    chunk_size = max(1, total_size // max_chunks)
    chunks = [None] * max_chunks
    errors = [None] * max_chunks
    lock = threading.Lock()

    def fetch(idx):
        start = idx * chunk_size
        end = start + chunk_size - 1 if idx < max_chunks - 1 else total_size - 1
        headers = {'Range': f'bytes={start}-{end}'}
        try:
            resp = session.get(url, headers=headers, timeout=TIMEOUT)
            if resp.status_code not in (200, 206):
                raise Exception(f'Status {resp.status_code}')
            with lock:
                chunks[idx] = resp.content
        except Exception as e:
            errors[idx] = e

    threads = []
    for i in range(max_chunks):
        t = threading.Thread(target=fetch, args=(i,))
        threads.append(t)
        t.start()
    for t in threads:
        t.join()

    if any(e for e in errors if e):
        raise Exception("Chunked download failed")

    data = b''.join(chunks)
    return data, filename


def download_all(urls, max_parallel=5):
    results = {}
    lock = threading.Lock()
    sem = threading.Semaphore(max_parallel)

    def worker(u):
        with sem:
            try:
                data, fname = download_file(u)
                with lock:
                    if fname in results:
                        fname = f"{int(time.time()*1000)}_{fname}"
                    results[fname] = data
            except Exception as e:
                print(f"Error downloading {u}: {e}")

    threads = [threading.Thread(target=worker, args=(u,)) for u in urls]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return results


def create_zip(files):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, 'w', zipfile.ZIP_DEFLATED) as zf:
        for name, data in files.items():
            zf.writestr(name, data)
    buf.seek(0)
    return buf.getvalue()