import os
import re
import time
import json
import base64
import requests
import threading
from download import download_all, create_zip

TELEGRAM_TOKEN = os.environ.get("TELEGRAM_TOKEN")
GITHUB_TOKEN = os.environ.get("GITHUB_TOKEN")
OWNER = os.environ.get("REPO_OWNER")
REPO = os.environ.get("REPO_NAME")

BASE_URL = "https://tapi.bale.ai/bot"
GITHUB_API = "https://api.github.com"

OFFSET_FILE = "offset.txt"
DOWNLOADS_DIR = "downloads"

session = requests.Session()
session.headers.update({"User-Agent": "GitHubBot/1.0"})


def get_offset():
    try:
        with open(OFFSET_FILE, 'r') as f:
            return int(f.read().strip())
    except:
        return 0


def save_offset(offset):
    with open(OFFSET_FILE, 'w') as f:
        f.write(str(offset))


def commit_file_to_repo(path, content, sha=None):
    api_url = f"{GITHUB_API}/repos/{OWNER}/{REPO}/contents/{path}"
    headers = {"Authorization": f"token {GITHUB_TOKEN}"}
    if sha is None:
        try:
            r = session.get(api_url, headers=headers)
            if r.status_code == 200:
                sha = r.json().get('sha')
        except:
            pass
    payload = {
        "message": f"Update {path}",
        "content": base64.b64encode(content).decode(),
        "branch": "main"
    }
    if sha:
        payload["sha"] = sha
    resp = session.put(api_url, json=payload, headers=headers)
    resp.raise_for_status()


def send_message(chat_id, text):
    url = f"{BASE_URL}{TELEGRAM_TOKEN}/sendMessage"
    payload = {"chat_id": chat_id, "text": text}
    try:
        session.post(url, json=payload)
    except Exception as e:
        print(f"Send message error: {e}")


def get_updates(offset):
    url = f"{BASE_URL}{TELEGRAM_TOKEN}/getUpdates"
    params = {"offset": offset, "timeout": 5}
    try:
        resp = session.get(url, params=params, timeout=10)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        print(f"getUpdates error: {e}")
        return {"ok": False}


def handle_message(chat_id, text):
    text = text.strip()

    if text == "/start":
        send_message(chat_id, "سلام! لینک بده دانلود کنم.\n"
                              "/list → لیست فایل‌ها\n"
                              "/simple, /zipall, /zipeach → حالت دانلود")
        return
    if text == "/help":
        send_message(chat_id, "/simple + لینک‌ها = ساده\n"
                              "/zipall + لینک‌ها = همه در یک zip\n"
                              "/zipeach + لینک‌ها = هر فایل zip جدا\n"
                              "/list = لیست فایل‌ها\n"
                              "/cancel = لغو (همان چرخه)")
        return
    if text == "/list":
        list_files(chat_id)
        return

    mode = None
    text_lower = text.lower()
    if text_lower.startswith("/simple"):
        mode = "simple"
        text = text[len("/simple"):].strip()
    elif text_lower.startswith("/zipall"):
        mode = "zipall"
        text = text[len("/zipall"):].strip()
    elif text_lower.startswith("/zipeach"):
        mode = "zipeach"
        text = text[len("/zipeach"):].strip()

    urls = re.findall(r'https?://[^\s]+', text)
    if not urls:
        send_message(chat_id, "❌ هیچ لینکی پیدا نشد.")
        return

    if mode is None:
        mode = "simple" if len(urls) == 1 else "zipall"

    send_message(chat_id, "⏳ در حال دانلود...")

    try:
        files_dict = download_all(urls)
        if not files_dict:
            send_message(chat_id, "❌ هیچ فایلی دانلود نشد.")
            return
        if mode == "simple":
            commit_simple(chat_id, files_dict)
        elif mode == "zipall":
            commit_zipall(chat_id, files_dict)
        elif mode == "zipeach":
            commit_zipeach(chat_id, files_dict)
    except Exception as e:
        send_message(chat_id, f"❌ خطا: {e}")


def commit_simple(chat_id, files):
    for fname, data in files.items():
        path = f"{DOWNLOADS_DIR}/{fname}"
        try:
            commit_file_to_repo(path, data)
            raw = f"https://raw.githubusercontent.com/{OWNER}/{REPO}/main/{path}"
            send_message(chat_id, f"✅ {raw}")
        except Exception as e:
            send_message(chat_id, f"❌ خطا در ذخیره {fname}: {e}")


def commit_zipall(chat_id, files):
    zip_data = create_zip(files)
    zip_name = f"archive_{int(time.time())}.zip"
    path = f"{DOWNLOADS_DIR}/{zip_name}"
    try:
        commit_file_to_repo(path, zip_data)
        raw = f"https://raw.githubusercontent.com/{OWNER}/{REPO}/main/{path}"
        send_message(chat_id, f"✅ {raw}")
    except Exception as e:
        send_message(chat_id, f"❌ خطا: {e}")


def commit_zipeach(chat_id, files):
    suf = int(time.time())
    for fname, data in files.items():
        zip_data = create_zip({fname: data})
        stem = re.sub(r'\.[^.]+$', '', fname)
        zip_name = f"{stem}_{suf}.zip"
        path = f"{DOWNLOADS_DIR}/{zip_name}"
        try:
            commit_file_to_repo(path, zip_data)
            raw = f"https://raw.githubusercontent.com/{OWNER}/{REPO}/main/{path}"
            send_message(chat_id, f"✅ {raw}")
        except Exception as e:
            send_message(chat_id, f"❌ خطا: {e}")


def list_files(chat_id):
    url = f"{GITHUB_API}/repos/{OWNER}/{REPO}/contents/{DOWNLOADS_DIR}"
    headers = {"Authorization": f"token {GITHUB_TOKEN}"}
    try:
        r = session.get(url, headers=headers)
        r.raise_for_status()
        items = r.json()
        if not items:
            send_message(chat_id, "پوشه خالی است.")
            return
        msg = "📂 فایل‌ها:\n"
        for item in items:
            if item.get("type") == "file":
                msg += f"• {item['name']}\n  {item['download_url']}\n"
        send_message(chat_id, msg)
    except Exception:
        send_message(chat_id, "❌ خطا در گرفتن لیست.")


def main():
    offset = get_offset()

    resp = get_updates(offset)
    if not resp.get("ok"):
        return
    results = resp.get("result", [])
    if not results:
        return

    cancel_chats = set()
    for upd in results:
        msg = upd.get("message")
        if msg and msg.get("text") and msg["text"].strip() == "/cancel":
            cancel_chats.add(msg["chat"]["id"])

    for upd in results:
        offset = upd["update_id"] + 1
        save_offset(offset)

        msg = upd.get("message")
        if not msg:
            continue
        chat_id = msg["chat"]["id"]
        text = msg.get("text", "")

        if text.strip() == "/cancel":
            continue
        if chat_id in cancel_chats:
            send_message(chat_id, "درخواست قبلی لغو شد.")
            continue

        handle_message(chat_id, text)


if __name__ == "__main__":
    main()