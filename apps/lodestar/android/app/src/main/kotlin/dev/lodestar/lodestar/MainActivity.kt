package dev.lodestar.lodestar

import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        // Background location dies on most phones unless the app is
        // whitelisted from battery optimization (stock Doze and OEM
        // battery managers both freeze background apps). Exposing the
        // standard system dialog is a one-tap ask in the tracking flow.
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "dev.lodestar/battery")
            .setMethodCallHandler { call, result ->
                val pm = getSystemService(POWER_SERVICE) as PowerManager
                when (call.method) {
                    "isIgnoringBatteryOptimizations" ->
                        result.success(pm.isIgnoringBatteryOptimizations(packageName))
                    "requestIgnoreBatteryOptimizations" -> {
                        if (!pm.isIgnoringBatteryOptimizations(packageName)) {
                            startActivity(
                                Intent(
                                    Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                                    Uri.parse("package:$packageName")
                                )
                            )
                        }
                        result.success(null)
                    }
                    else -> result.notImplemented()
                }
            }
    }
}