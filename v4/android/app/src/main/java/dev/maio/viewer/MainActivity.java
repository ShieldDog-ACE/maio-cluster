package dev.maio.viewer;

import android.app.Activity;
import android.content.Context;
import android.content.SharedPreferences;
import android.graphics.Color;
import android.graphics.Typeface;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.InputType;
import android.text.TextUtils;
import android.util.Base64;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * maio v4 quick-view client.
 *
 * The APK is intentionally read-only: it lists cluster members, models and
 * jobs. Model installation and distributed inference stay on the desktop
 * nodes. All endpoints are plain HTTP on a trusted LAN/USB/hotspot link.
 */
public class MainActivity extends Activity {
    private static final String PREFS = "maio";
    private static final String KEY_URL = "url";
    private static final String KEY_TOKEN = "token";
    private static final int REFRESH_MS = 5000;

    private EditText urlInput;
    private EditText tokenInput;
    private TextView statusView;
    private TextView contentView;
    private Button connectButton;
    private Button refreshButton;

    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private final Handler ui = new Handler(Looper.getMainLooper());
    private final Runnable refresher = new Runnable() {
        @Override
        public void run() {
            refresh(false);
            ui.postDelayed(this, REFRESH_MS);
        }
    };

    private String baseUrl = "";
    private String token = "";
    private volatile boolean busy = false;
    private volatile boolean resumed = false;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        SharedPreferences sp = getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        baseUrl = sp.getString(KEY_URL, "http://127.0.0.1:47800");
        token = sp.getString(KEY_TOKEN, "");
        setContentView(buildUi());
        urlInput.setText(baseUrl);
        tokenInput.setText(token);
        connectButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                connect();
            }
        });
        refreshButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                refresh(true);
            }
        });
    }

    private View buildUi() {
        int pad = dp(16);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(pad, pad, pad, 0);
        root.setBackgroundColor(Color.rgb(248, 250, 252));

        TextView title = new TextView(this);
        title.setText("maio cluster");
        title.setTextSize(24);
        title.setTypeface(Typeface.DEFAULT_BOLD);
        title.setTextColor(Color.rgb(15, 118, 110));
        root.addView(title);

        TextView subtitle = new TextView(this);
        subtitle.setText("局域网 / 网线 / USB / 热点  ·  快速查看");
        subtitle.setTextSize(12);
        subtitle.setTextColor(Color.rgb(71, 85, 105));
        LinearLayout.LayoutParams subLp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        subLp.bottomMargin = dp(10);
        root.addView(subtitle, subLp);

        urlInput = new EditText(this);
        urlInput.setHint("http://192.168.x.x:47800 或 MAIO1-...");
        urlInput.setSingleLine(true);
        urlInput.setTextSize(13);
        urlInput.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        root.addView(urlInput, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        tokenInput = new EditText(this);
        tokenInput.setHint("cluster token");
        tokenInput.setSingleLine(true);
        tokenInput.setTextSize(13);
        tokenInput.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        root.addView(tokenInput, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        LinearLayout buttons = new LinearLayout(this);
        buttons.setOrientation(LinearLayout.HORIZONTAL);
        buttons.setGravity(Gravity.CENTER_VERTICAL);
        connectButton = new Button(this);
        connectButton.setText("连接");
        refreshButton = new Button(this);
        refreshButton.setText("刷新");
        buttons.addView(connectButton, new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        buttons.addView(refreshButton, new LinearLayout.LayoutParams(0,
                ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        root.addView(buttons);

        statusView = new TextView(this);
        statusView.setTextSize(12);
        statusView.setTextColor(Color.rgb(71, 85, 105));
        statusView.setPadding(0, dp(4), 0, dp(8));
        statusView.setText("未连接");
        root.addView(statusView);

        ScrollView scroll = new ScrollView(this);
        contentView = new TextView(this);
        contentView.setTextSize(12);
        contentView.setTypeface(Typeface.MONOSPACE);
        contentView.setTextColor(Color.rgb(15, 23, 42));
        contentView.setTextIsSelectable(true);
        contentView.setPadding(0, 0, 0, dp(24));
        contentView.setText("点击“连接”读取集群状态。\n\n"
                + "USB 数据线：在电脑上执行 maio link usb，\n然后这里填 http://127.0.0.1:47800。\n"
                + "无线/网线/热点：填电脑的局域网地址。");
        scroll.addView(contentView, new ScrollView.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        root.addView(scroll, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1));
        return root;
    }

    private void connect() {
        String raw = urlInput.getText().toString().trim();
        if (raw.startsWith("MAIO1-")) {
            if (!parseJoinCode(raw)) {
                return;
            }
            urlInput.setText(baseUrl);
            tokenInput.setText(token);
        } else {
            if (!raw.contains("://")) {
                raw = "http://" + raw;
            }
            baseUrl = raw.replaceAll("/+$", "");
            token = tokenInput.getText().toString().trim();
        }
        SharedPreferences.Editor e = getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit();
        e.putString(KEY_URL, baseUrl);
        e.putString(KEY_TOKEN, token);
        e.apply();
        refresh(true);
    }

    private boolean parseJoinCode(String code) {
        try {
            String payload = code.substring("MAIO1-".length());
            byte[] raw = Base64.decode(payload, Base64.URL_SAFE | Base64.NO_WRAP | Base64.NO_PADDING);
            JSONObject o = new JSONObject(new String(raw, StandardCharsets.UTF_8));
            String host = o.optString("h");
            int port = o.optInt("p", 47800);
            token = o.optString("t");
            if (host.isEmpty() || token.isEmpty()) {
                toast("配对码缺少地址或 token");
                return false;
            }
            if (host.contains(":")) {
                baseUrl = "http://[" + host + "]:" + port;
            } else {
                baseUrl = "http://" + host + ":" + port;
            }
            return true;
        } catch (Exception ex) {
            toast("配对码格式错误");
            return false;
        }
    }

    private void refresh(final boolean manual) {
        if (busy) {
            return;
        }
        busy = true;
        if (manual) {
            statusView.setText("读取中…");
        }
        final String url = baseUrl;
        final String tok = token;
        io.execute(new Runnable() {
            @Override
            public void run() {
                String text;
                String state;
                try {
                    JSONObject info = new JSONObject(httpGet(url + "/v1/info", null));
                    JSONArray nodes = new JSONObject(httpGet(url + "/v1/nodes", tok)).optJSONArray("nodes");
                    JSONArray models = new JSONObject(httpGet(url + "/v1/models", tok)).optJSONArray("models");
                    JSONArray jobs = new JSONObject(httpGet(url + "/v1/jobs", tok)).optJSONArray("jobs");
                    text = render(info, nodes, models, jobs);
                    state = "已连接 · " + info.optString("name") + " · " + info.optInt("nodes") + " 节点";
                } catch (Exception ex) {
                    text = "连接失败\n\n" + ex.getMessage()
                            + "\n\n检查：\n"
                            + "1. 电脑上 maio cluster up 正在运行\n"
                            + "2. 手机与电脑在同一 Wi-Fi/热点/网线网段\n"
                            + "3. token 来自 maio cluster join-code\n"
                            + "4. USB 模式先在电脑执行 maio link usb";
                    state = "未连接";
                }
                final String fText = text;
                final String fState = state;
                ui.post(new Runnable() {
                    @Override
                    public void run() {
                        contentView.setText(fText);
                        statusView.setText(fState);
                        busy = false;
                    }
                });
            }
        });
    }

    private String render(JSONObject info, JSONArray nodes, JSONArray models, JSONArray jobs) {
        StringBuilder b = new StringBuilder();
        b.append("集群  ").append(info.optString("name")).append("  (")
                .append(info.optString("id")).append(")\n");
        b.append("协议  ").append(info.optInt("protocol"))
                .append("    节点  ").append(info.optInt("nodes"))
                .append("    端口  ").append(info.optInt("port")).append("\n\n");

        b.append("节点\n");
        if (nodes == null || nodes.length() == 0) {
            b.append("  (无)\n");
        } else {
            for (int i = 0; i < nodes.length(); i++) {
                JSONObject n = nodes.optJSONObject(i);
                if (n == null) {
                    continue;
                }
                b.append("  ● ").append(n.optString("name"));
                String role = n.optString("role");
                if ("coordinator".equals(role)) {
                    b.append(" [协调]");
                }
                b.append("  ").append(n.optString("os")).append("/")
                        .append(n.optString("arch")).append("  ")
                        .append(n.optString("state")).append("\n");
                JSONObject hw = n.optJSONObject("hardware");
                if (hw != null) {
                    b.append("      CPU ").append(hw.optInt("cpu_cores")).append(" cores")
                            .append("   RAM ").append(bytes(hw.optLong("mem_total"))).append("\n");
                    JSONArray gpus = hw.optJSONArray("gpus");
                    if (gpus != null && gpus.length() > 0) {
                        for (int g = 0; g < gpus.length(); g++) {
                            JSONObject gpu = gpus.optJSONObject(g);
                            if (gpu == null) {
                                continue;
                            }
                            b.append("      GPU ").append(gpu.optString("name"))
                                    .append("  ").append(bytes(gpu.optLong("vram_total")))
                                    .append(" (").append(gpu.optString("backend")).append(")\n");
                        }
                    } else {
                        b.append("      GPU 无（CPU 参与计算）\n");
                    }
                }
                JSONObject rpc = n.optJSONObject("rpc");
                b.append("      地址 ").append(n.optString("host"));
                if (n.optInt("api_port") > 0) {
                    b.append(":").append(n.optInt("api_port"));
                }
                b.append("    RPC ").append(rpc != null && rpc.optBoolean("available") ? "可用" : "不可用")
                        .append("\n\n");
            }
        }

        b.append("模型\n");
        if (models == null || models.length() == 0) {
            b.append("  (无)\n\n");
        } else {
            for (int i = 0; i < models.length(); i++) {
                JSONObject m = models.optJSONObject(i);
                if (m == null) {
                    continue;
                }
                b.append("  ").append(m.optString("name"))
                        .append("  ").append(bytes(m.optLong("size_bytes")))
                        .append("  ").append(m.optInt("layers")).append(" layers");
                JSONArray on = m.optJSONArray("nodes");
                if (on != null && on.length() > 0) {
                    b.append("  @");
                    for (int k = 0; k < on.length(); k++) {
                        if (k > 0) {
                            b.append(",");
                        }
                        b.append(on.optString(k));
                    }
                }
                b.append("\n");
            }
            b.append("\n");
        }

        b.append("任务\n");
        if (jobs == null || jobs.length() == 0) {
            b.append("  (无)\n");
        } else {
            int shown = 0;
            for (int i = jobs.length() - 1; i >= 0 && shown < 8; i--, shown++) {
                JSONObject j = jobs.optJSONObject(i);
                if (j == null) {
                    continue;
                }
                b.append("  ").append(j.optString("state")).append("  ")
                        .append(j.optString("model")).append("  ")
                        .append(j.optString("mode")).append("  ");
                String out = j.optString("output").replace("\n", " ");
                if (out.length() > 48) {
                    out = out.substring(0, 48) + "…";
                }
                b.append(out).append("\n");
            }
        }
        return b.toString();
    }

    private String httpGet(String url, String tok) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setConnectTimeout(5000);
        c.setReadTimeout(10000);
        c.setRequestMethod("GET");
        c.setRequestProperty("Accept", "application/json");
        if (!TextUtils.isEmpty(tok)) {
            c.setRequestProperty("Authorization", "Bearer " + tok);
        }
        int code = c.getResponseCode();
        InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
        StringBuilder body = new StringBuilder();
        if (in != null) {
            BufferedReader r = new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8));
            String line;
            while ((line = r.readLine()) != null) {
                body.append(line);
            }
            r.close();
        }
        c.disconnect();
        if (code >= 400) {
            String msg = body.toString();
            try {
                msg = new JSONObject(msg).optString("error", msg);
            } catch (Exception ignored) {
            }
            throw new Exception("HTTP " + code + ": " + msg);
        }
        return body.toString();
    }

    private static String bytes(long n) {
        if (n <= 0) {
            return "0 B";
        }
        String[] units = {"B", "KiB", "MiB", "GiB", "TiB"};
        double v = n;
        int u = 0;
        while (v >= 1024 && u < units.length - 1) {
            v /= 1024;
            u++;
        }
        return String.format(java.util.Locale.US, "%.1f %s", v, units[u]);
    }

    private int dp(int v) {
        return (int) (v * getResources().getDisplayMetrics().density + 0.5f);
    }

    private void toast(String msg) {
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show();
    }

    @Override
    protected void onResume() {
        super.onResume();
        resumed = true;
        ui.post(refresher);
    }

    @Override
    protected void onPause() {
        resumed = false;
        ui.removeCallbacks(refresher);
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        io.shutdownNow();
        super.onDestroy();
    }
}
