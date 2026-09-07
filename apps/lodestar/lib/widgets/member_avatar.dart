import 'package:flutter/material.dart';

/// A colored circle with the member's initial — the visual identity of a
/// circle member on the map and in lists.
class MemberAvatar extends StatelessWidget {
  const MemberAvatar({
    super.key,
    required this.name,
    required this.color,
    this.size = 40,
    this.showPause = false,
  });

  final String name;
  final String color;
  final double size;
  final bool showPause;

  @override
  Widget build(BuildContext context) {
    // Use runes, not [0]: an emoji name is a surrogate pair and name[0]
    // would split it into a lone surrogate that renders as garbage.
    final initial = name.isEmpty
        ? '?'
        : String.fromCharCode(name.runes.first).toUpperCase();
    return Stack(
      alignment: Alignment.center,
      children: [
        Container(
          width: size,
          height: size,
          decoration: BoxDecoration(
            color: _parseColor(color),
            shape: BoxShape.circle,
            border: Border.all(color: Colors.white, width: 2),
            boxShadow: const [
              BoxShadow(
                color: Colors.black26,
                blurRadius: 4,
                offset: Offset(0, 1),
              ),
            ],
          ),
          alignment: Alignment.center,
          child: Text(
            initial,
            style: TextStyle(
              color: Colors.white,
              fontSize: size * 0.45,
              fontWeight: FontWeight.bold,
            ),
          ),
        ),
        if (showPause)
          Positioned(
            right: 0,
            bottom: 0,
            child: Container(
              padding: const EdgeInsets.all(2),
              decoration: const BoxDecoration(
                color: Colors.black87,
                shape: BoxShape.circle,
              ),
              child: Icon(Icons.pause, size: size * 0.35, color: Colors.white),
            ),
          ),
      ],
    );
  }

  static Color _parseColor(String hex) {
    var h = hex.replaceAll('#', '');
    if (h.length == 6) h = 'FF$h';
    final v = int.tryParse(h, radix: 16) ?? 0xFF4F7CFF;
    return Color(v);
  }
}
