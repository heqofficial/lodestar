import 'dart:async';

import 'package:flutter/material.dart';
import 'package:provider/provider.dart';

import 'screens/chat_screen.dart';
import 'screens/circles_screen.dart';
import 'screens/history_screen.dart';
import 'screens/map_screen.dart';
import 'screens/members_screen.dart';
import 'screens/onboarding_screen.dart';
import 'screens/places_screen.dart';
import 'screens/settings_screen.dart';
import 'screens/sos_screen.dart';
import 'screens/trips_screen.dart';
import 'state/app_state.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final state = AppState();
  // Identity/settings come from local storage only — fast. The network sync
  // (circles, members, keys) runs in the background behind a loading state
  // so a slow link can never leave the user staring at a blank screen.
  await state.init();
  runApp(LodestarApp(state: state));
  if (state.registered) {
    unawaited(state.load());
  }
}

class LodestarApp extends StatelessWidget {
  const LodestarApp({super.key, required this.state});

  final AppState state;

  @override
  Widget build(BuildContext context) {
    return ChangeNotifierProvider.value(
      value: state,
      child: MaterialApp(
        title: 'Lodestar',
        debugShowCheckedModeBanner: false,
        theme: _theme(),
        initialRoute: state.registered ? '/' : '/onboarding',
        routes: {
          '/onboarding': (_) => const OnboardingScreen(),
          '/': (_) => const CirclesScreen(),
          '/map': (_) => const MapScreen(),
          '/places': (_) => const PlacesScreen(),
          '/chat': (_) => const ChatScreen(),
          '/sos': (_) => const SosScreen(),
          '/settings': (_) => const SettingsScreen(),
          '/members': (_) => const MembersScreen(),
        },
        onGenerateRoute: (settings) {
          // Routes that accept a member id argument (deviceId string).
          if (settings.name == '/history') {
            return MaterialPageRoute(
              builder: (_) =>
                  HistoryScreen(initialMemberId: settings.arguments as String?),
            );
          }
          if (settings.name == '/trips') {
            return MaterialPageRoute(
              builder: (_) =>
                  TripsScreen(initialMemberId: settings.arguments as String?),
            );
          }
          return null;
        },
      ),
    );
  }

  ThemeData _theme() {
    final scheme = ColorScheme.fromSeed(seedColor: const Color(0xFF4F7CFF));
    return ThemeData(
      colorScheme: scheme,
      useMaterial3: true,
      appBarTheme: AppBarTheme(
        backgroundColor: scheme.surface,
        elevation: 0,
        scrolledUnderElevation: 1,
      ),
      cardTheme: const CardThemeData(
        elevation: 0.5,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.all(Radius.circular(14)),
        ),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: FilledButton.styleFrom(
          minimumSize: const Size(0, 48),
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(12),
          ),
        ),
      ),
    );
  }
}
